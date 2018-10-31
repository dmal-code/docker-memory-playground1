On the first look Docker containers seem to induce nearly no overhead, on the contrary they seem to use less memory.
It is crucial to include the linux COW semantics in order to measure the used container memory. 
The main goal of this article is to provide an insight for how to measure memory of dockerized applications and the pitfalls 
when doing so. Let us consider the following small go programm:

main.go
```go
package main

import (
  "encoding/json"
  "strconv"
  "fmt"
  "github.com/gorilla/mux"
  "log"
  "net/http"
  "runtime"
)

import unsafe "unsafe"

type Entity struct {
	ID      string    `json:"id,omitempty"`
	Payload []float64 `json:"[],omitempty"`
}

var entities []Entity

func bToMb(b uint64) uint64 {
    return b / 1024 / 1024
}

func printStats() {
  var m runtime.MemStats
  runtime.ReadMemStats(&m)    
  fmt.Printf("Alloc = %v MiB", bToMb(m.Alloc))
  fmt.Printf("\tHeapAlloc = %v MiB", bToMb(m.HeapAlloc))
  fmt.Printf("\tTotalAlloc = %v MiB", bToMb(m.TotalAlloc))
  fmt.Printf("\tStackSys = %v MiB", bToMb(m.StackSys))
  fmt.Printf("\tHeapSys = %v MiB", bToMb(m.HeapSys))
  fmt.Printf("\tSys = %v MiB", bToMb(m.Sys))
  fmt.Printf("\tNumGC = %v\n", m.NumGC)
}

func createEntities(w http.ResponseWriter, r *http.Request) {
  params := mux.Vars(r)
 
  entities = nil
  runtime.GC()
  fmt.Println("Creating data")
  amount,_ := strconv.ParseInt(params["count"], 10, 32)
  if amount == 0 {
    fmt.Println("Error: amount was 0")
    return
  } 
  
  entities := make([]Entity, amount)
  
  fmt.Println("Created data")    
  allocatedBytes := ( int(unsafe.Sizeof(entities[0])) *int(amount))
  json.NewEncoder(w).Encode("allocated: "+ strconv.Itoa(allocatedBytes) + " bytes")
  runtime.GC()
  printStats()
  return
}

func main() {
	router := mux.NewRouter()
	router.HandleFunc("/entity/{count}", createEntities).Methods("GET")
	log.Fatal(http.ListenAndServe(":8000", router))
}
```
and wrapper scripts 

run.sh
```bash
#!/bin/bash
docker build --network="host" -t docker_go .
docker-compose up go
docker-compose rm --force go
docker container prune -f
```

Dockerfile
```bash
FROM golang:1.8
WORKDIR /go/src/app
COPY ./src/* /go/src/app/
RUN go get -d -v ./...
RUN go install -v ./...
CMD app
```

docker-compose.yml
```yaml
version: '2.0'

services:
  go:
    image: docker_go
    mem_limit: 128m
    ports:
      - 899:8000
```

The control group will restrict the containerized application to 151340 KiB vmsize and add an OOM limit of 134217728 bytes. 
This can be seen by issuing the commands: 
cat /proc/PID_OF_DOCKER_APP/cgroup
cgget -g memory:/docker/2a1512b70df5d1bdef7d5bfd2145e48521004dfb9e9fa8fe30daad7dc75583c2 (<- obtained from the the cgroup list).
The interesting entry here is: memory.limit_in_bytes: 134217728.
One can see that the process itself is using 2625536 bytes of memory: memory.usage_in_bytes: 2625536
While the vmsize is simply calculated by adding up all mapped memory (virtual memory range + all mapped files, thus it may be larger 
than the available docker/physical memory), one can check this number via 'cat /proc/PID_OF_DOCKER_APP/status | grep VmSize'. 
At this point one should be reminded about the possibility of using swap memory in order to increase the available physical memory.
Once we call the endpoint 'http://172.17.171.171:899/entity/3000000' we notice that 'memory.memsw.usage_in_bytes: 6701056' while 
on the second call it will jump to 'memory.memsw.usage_in_bytes: 126803968' which is the result of a modifying memory access by the 
kernel ('entities = nil;runtime.GC()'). 
In order to see the overhead we do a simple calculation: a total of 3000000 entites, each of size 40 bytes, were allocated on the heap. 
This yields 120000000 bytes, the application will report that 121 MiB were requested from the (host-)system, this is correct so far.
Comparing this with the cgroup we see a difference of 126976000-120000000 = 6976000 = 6MiB, which is nearly identical with the initial 
memory when we started the application. From that we can deduce that the actual container memory overhead is pretty much negligible, 
from this numbers and our analysis methology we even can't be sure if it is docker overhead at all since it might be normal variance 
of a linux process.  
How does the process behave in a non-docker environment? Compiling the source file on the host and running it yields interesting numbers 
e.g. vmsize: 123268 KiB, RSS: 22664 KiB. Furthermore, after the first call to 'http://172.17.171.171:8000/entity/3000000' we get 
vmsize: 361120 KiB, RSS: 159728 KiB, which is interesting for two reasons. First we increased the RSS memory with the first call (no COW), secondly we also increase the VmSize. The application itself states to have obtained 122 MiB from the system.
Let us compare the numbers, the RSS delta is 159728 - 22664 = 137064 KiB = 133 MiB. This increase is larger than in the docker context. 
The increased VmSize indicates that the system gave the application more virtual space than it currently needs, yet it only allocated the fragment which is required to hold the Entity array. Such an increase makes no sense in the docker environment since the cgroup wouldn't allow the allocation of VmSize bytes. Issuing a 'cat /proc/PID_OF_APP/maps' shows that much more memory segments are mapped to shared libraries, which explains the larger vmsize and RSS value.
On the host as well as in the docker environment the resource stats appear to be idempotent for subsequent REST calls. The dockerized application seems to have a slimmer memory footprint when started due to the environment or available libraries within the docker image. This assumption can be verified by comparing the footprint with a statically linked version of the go application ('go build -compiler gccgo -gccgoflags '-static' main.go').Here we get VmSize: 102224 KiB and RSS 8112 KiB.
Thinking ahead one develops the question if this may also be the reason for the COW-like memory allocation, i.e. if the docker environment provides a version of libgo or glibc which uses COW semantics. This question can be answered by simply using the statically linked version within the docker environment.
The dockerfile must be slightly adapted for this

Dockerfile2
```bash
FROM golang:1.8
WORKDIR /go/src/app
COPY ./src/* /go/src/app/
CMD ./main
```

Indeed the statically linked binary exhibits the same behaviour as on the host, i.e. it allocates the memory with the first call. If we now compile a 
dynamically linked binary within the container and copy it into the host we will see the same COW behaviour as before (it even states to have claimed 121 MiB). 
*In order to get the file, simply start the previous service and copy it from /go/bin/app to the host.*
Which is incredible interesting regarding the question which elements induce the different behaviour? 
By studying the binary with 'ldd -d main' we get 
```bash
linux-vdso.so.1 (0x00007ffd1d4ee000)
libpthread.so.0 => /usr/lib/libpthread.so.0 (0x00007feffab5e000)
libc.so.6 => /usr/lib/libc.so.6 (0x00007feffa99a000)
/lib64/ld-linux-x86-64.so.2 => /usr/lib64/ld-linux-x86-64.so.2 (0x00007feffab95000)
```
where as the host compiled binary yields 
```bash
linux-vdso.so.1 (0x00007fff6af4e000)
libgo.so.13 => /usr/lib/libgo.so.13 (0x00007fdd5924e000)
libm.so.6 => /usr/lib/libm.so.6 (0x00007fdd590c9000)
libgcc_s.so.1 => /usr/lib/libgcc_s.so.1 (0x00007fdd590af000)
libc.so.6 => /usr/lib/libc.so.6 (0x00007fdd58eeb000)
/lib64/ld-linux-x86-64.so.2 => /usr/lib64/ld-linux-x86-64.so.2 (0x00007fdd5aaf0000)
libz.so.1 => /usr/lib/libz.so.1 (0x00007fdd58cd4000)
libpthread.so.0 => /usr/lib/libpthread.so.0 (0x00007fdd58cb3000)
```	
The host version differs significantly, even when including libgo via ' go build -compiler gccgo -gccgoflags '-static-libgo' main.go' we still get
```bash
linux-vdso.so.1 (0x00007ffc3294b000)
libpthread.so.0 => /usr/lib/libpthread.so.0 (0x00007f8889eab000)
libm.so.6 => /usr/lib/libm.so.6 (0x00007f8889d26000)
libgcc_s.so.1 => /usr/lib/libgcc_s.so.1 (0x00007f8889d0c000)
libc.so.6 => /usr/lib/libc.so.6 (0x00007f8889b48000)
/lib64/ld-linux-x86-64.so.2 => /usr/lib64/ld-linux-x86-64.so.2 (0x00007f888ab41000)
```
Apparently we can't get rid of libm and libgcc on the host system and thus can't adapt our host compiled binary to match the docker compiled one in terms of external 
dependencies.
Nevertheless we can safely state at this position that docker itself does not induce any large overhead through the container environment, yet depending on the application 
one might see different behaviour in resource consumption simply because the application runs in a different environment.  

Docker:
```bash
00400000-0063a000 r-xp 00000000 08:01 1322550                            /go/bin/app
0063a000-007e5000 r--p 0023a000 08:01 1322550                            /go/bin/app
007e5000-00816000 rw-p 003e5000 08:01 1322550                            /go/bin/app
00816000-00838000 rw-p 00000000 00:00 0
01790000-017b1000 rw-p 00000000 00:00 0                                  [heap]
c000000000-c000001000 rw-p 00000000 00:00 0
c41fff8000-c420000000 rw-p 00000000 00:00 0
c420000000-c420100000 rw-p 00000000 00:00 0
7f18233a6000-7f18233a7000 ---p 00000000 00:00 0
7f18233a7000-7f1823ba7000 rw-p 00000000 00:00 0
7f1823ba7000-7f1823ba8000 ---p 00000000 00:00 0
7f1823ba8000-7f18243a8000 rw-p 00000000 00:00 0
7f18243a8000-7f1824549000 r-xp 00000000 08:01 1318143                    /lib/x86_64-linux-gnu/libc-2.19.so
7f1824549000-7f1824749000 ---p 001a1000 08:01 1318143                    /lib/x86_64-linux-gnu/libc-2.19.so
7f1824749000-7f182474d000 r--p 001a1000 08:01 1318143                    /lib/x86_64-linux-gnu/libc-2.19.so
7f182474d000-7f182474f000 rw-p 001a5000 08:01 1318143                    /lib/x86_64-linux-gnu/libc-2.19.so
7f182474f000-7f1824753000 rw-p 00000000 00:00 0
7f1824753000-7f182476b000 r-xp 00000000 08:01 1318213                    /lib/x86_64-linux-gnu/libpthread-2.19.so
7f182476b000-7f182496a000 ---p 00018000 08:01 1318213                    /lib/x86_64-linux-gnu/libpthread-2.19.so
7f182496a000-7f182496b000 r--p 00017000 08:01 1318213                    /lib/x86_64-linux-gnu/libpthread-2.19.so
7f182496b000-7f182496c000 rw-p 00018000 08:01 1318213                    /lib/x86_64-linux-gnu/libpthread-2.19.so
7f182496c000-7f1824970000 rw-p 00000000 00:00 0
7f1824970000-7f1824991000 r-xp 00000000 08:01 1318124                    /lib/x86_64-linux-gnu/ld-2.19.so
7f1824ae7000-7f1824b8a000 rw-p 00000000 00:00 0
7f1824b8e000-7f1824b90000 rw-p 00000000 00:00 0
7f1824b90000-7f1824b91000 r--p 00020000 08:01 1318124                    /lib/x86_64-linux-gnu/ld-2.19.so
7f1824b91000-7f1824b92000 rw-p 00021000 08:01 1318124                    /lib/x86_64-linux-gnu/ld-2.19.so
7f1824b92000-7f1824b93000 rw-p 00000000 00:00 0
7ffec6c0c000-7ffec6c2d000 rw-p 00000000 00:00 0                          [stack]
7ffec6c6c000-7ffec6c6f000 r--p 00000000 00:00 0                          [vvar]
7ffec6c6f000-7ffec6c71000 r-xp 00000000 00:00 0                          [vdso]
```

Host:
```bash
c000000000-c00001e000 rw-p 00000000 00:00 0
c41fc5c000-c420000000 rw-p 00000000 00:00 0
c420000000-c420100000 rw-p 00000000 00:00 0
c420100000-c420200000 rw-p 00000000 00:00 0
c420200000-c427200000 rw-p 00000000 00:00 0
c427200000-c427480000 rw-p 00000000 00:00 0
55967356d000-559673586000 r--p 00000000 08:01 2184471                    /root/memory_allocator/src/main/main
559673586000-559673598000 r-xp 00019000 08:01 2184471                    /root/memory_allocator/src/main/main
559673598000-55967359e000 r--p 0002b000 08:01 2184471                    /root/memory_allocator/src/main/main
55967359e000-5596735a8000 r--p 00030000 08:01 2184471                    /root/memory_allocator/src/main/main
5596735a8000-5596735ad000 rw-p 0003a000 08:01 2184471                    /root/memory_allocator/src/main/main
55967498a000-5596749ab000 rw-p 00000000 00:00 0                          [heap]
7efcf8000000-7efcf8021000 rw-p 00000000 00:00 0
7efcf8021000-7efcfc000000 ---p 00000000 00:00 0
7efcfee94000-7efd00000000 r--p 01a4e000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd00000000-7efd00021000 rw-p 00000000 00:00 0
7efd00021000-7efd04000000 ---p 00000000 00:00 0
7efd04bc5000-7efd04f23000 rw-p 00000000 00:00 0
7efd04fa2000-7efd052e9000 rw-p 00000000 00:00 0
7efd052e9000-7efd054c5000 r--p 02f04000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd0553b000-7efd05590000 rw-p 00000000 00:00 0
7efd05590000-7efd055aa000 r--p 001ef000 08:01 133998                     /usr/lib/libc-2.28.so
7efd055ac000-7efd055c6000 rw-p 00000000 00:00 0
7efd055c6000-7efd055cc000 r--p 00020000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd055cc000-7efd055d5000 rw-p 00000000 00:00 0
7efd055d5000-7efd055d7000 r--p 0002d000 08:01 133987                     /usr/lib/ld-2.28.so
7efd055d9000-7efd055da000 rw-p 00000000 00:00 0
7efd055da000-7efd055db000 r--p 00001000 08:01 144585                     /usr/lib/libz.so.1.2.11
7efd055dc000-7efd05618000 rw-p 00000000 00:00 0
7efd05618000-7efd056cf000 r--p 00019000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd056cf000-7efd056d7000 rw-p 00000000 00:00 0
7efd056d7000-7efd056db000 r--p 00008000 08:01 134013                     /usr/lib/libm-2.28.so
7efd056db000-7efd056dc000 rw-p 00000000 00:00 0
7efd056dc000-7efd056df000 r--p 000d1000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd056df000-7efd05811000 rw-p 00000000 00:00 0
7efd05811000-7efd05821000 r--p 00050000 08:01 2184471                    /root/memory_allocator/src/main/main
7efd05821000-7efd05829000 rw-p 00000000 00:00 0
7efd05829000-7efd05833000 r--p 00069000 08:01 2184471                    /root/memory_allocator/src/main/main
7efd05833000-7efd05850000 rw-p 00000000 00:00 0
7efd05850000-7efd05851000 ---p 00000000 00:00 0
7efd05851000-7efd078a0000 rw-p 00000000 00:00 0
7efd078a0000-7efd078a1000 ---p 00000000 00:00 0
7efd078a1000-7efd08509000 rw-p 00000000 00:00 0
7efd08509000-7efd0850f000 r--p 00000000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd0850f000-7efd0851e000 r-xp 00006000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd0851e000-7efd08524000 r--p 00015000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd08524000-7efd08525000 r--p 0001a000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd08525000-7efd08526000 rw-p 0001b000 08:01 134042                     /usr/lib/libpthread-2.28.so
7efd08526000-7efd0852a000 rw-p 00000000 00:00 0
7efd0852a000-7efd08540000 r-xp 00000000 08:01 144585                     /usr/lib/libz.so.1.2.11
7efd08540000-7efd0873f000 ---p 00016000 08:01 144585                     /usr/lib/libz.so.1.2.11
7efd0873f000-7efd08740000 r--p 00015000 08:01 144585                     /usr/lib/libz.so.1.2.11
7efd08740000-7efd08741000 rw-p 00016000 08:01 144585                     /usr/lib/libz.so.1.2.11
7efd08741000-7efd08763000 r--p 00000000 08:01 133998                     /usr/lib/libc-2.28.so
7efd08763000-7efd088ae000 r-xp 00022000 08:01 133998                     /usr/lib/libc-2.28.so
7efd088ae000-7efd088fa000 r--p 0016d000 08:01 133998                     /usr/lib/libc-2.28.so
7efd088fa000-7efd088fb000 ---p 001b9000 08:01 133998                     /usr/lib/libc-2.28.so
7efd088fb000-7efd088ff000 r--p 001b9000 08:01 133998                     /usr/lib/libc-2.28.so
7efd088ff000-7efd08901000 rw-p 001bd000 08:01 133998                     /usr/lib/libc-2.28.so
7efd08901000-7efd08905000 rw-p 00000000 00:00 0
7efd08905000-7efd08908000 r--p 00000000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd08908000-7efd08919000 r-xp 00003000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd08919000-7efd0891c000 r--p 00014000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd0891c000-7efd0891d000 ---p 00017000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd0891d000-7efd0891e000 r--p 00017000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd0891e000-7efd0891f000 rw-p 00018000 08:01 134828                     /usr/lib/libgcc_s.so.1
7efd0891f000-7efd0892c000 r--p 00000000 08:01 134013                     /usr/lib/libm-2.28.so
7efd0892c000-7efd089cd000 r-xp 0000d000 08:01 134013                     /usr/lib/libm-2.28.so
7efd089cd000-7efd08aa2000 r--p 000ae000 08:01 134013                     /usr/lib/libm-2.28.so
7efd08aa2000-7efd08aa3000 r--p 00182000 08:01 134013                     /usr/lib/libm-2.28.so
7efd08aa3000-7efd08aa4000 rw-p 00183000 08:01 134013                     /usr/lib/libm-2.28.so
7efd08aa4000-7efd0952d000 r--p 00000000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd0952d000-7efd09aa7000 r-xp 00a89000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd09aa7000-7efd09d41000 r--p 01003000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd09d41000-7efd0a0a6000 r--p 0129c000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd0a0a6000-7efd0a2c2000 rw-p 01601000 08:01 134834                     /usr/lib/libgo.so.13.0.0
7efd0a2c2000-7efd0a2f3000 rw-p 00000000 00:00 0
7efd0a2f3000-7efd0a306000 rw-p 00000000 00:00 0
7efd0a306000-7efd0a308000 r--p 00000000 08:01 133987                     /usr/lib/ld-2.28.so
7efd0a308000-7efd0a327000 r-xp 00002000 08:01 133987                     /usr/lib/ld-2.28.so
7efd0a327000-7efd0a32f000 r--p 00021000 08:01 133987                     /usr/lib/ld-2.28.so
7efd0a32f000-7efd0a330000 r--p 00028000 08:01 133987                     /usr/lib/ld-2.28.so
7efd0a330000-7efd0a331000 rw-p 00029000 08:01 133987                     /usr/lib/ld-2.28.so
7efd0a331000-7efd0a332000 rw-p 00000000 00:00 0
7ffd81ef1000-7ffd81f12000 rw-p 00000000 00:00 0                          [stack]
7ffd81ff4000-7ffd81ff7000 r--p 00000000 00:00 0                          [vvar]
7ffd81ff7000-7ffd81ff9000 r-xp 00000000 00:00 0                          [vdso]
```

Host(statically linked):
```bash
00400000-00401000 r--p 00000000 08:01 2184471                            /root/memory_allocator/src/main/main
00401000-00824000 r-xp 00001000 08:01 2184471                            /root/memory_allocator/src/main/main
00824000-00a68000 r--p 00424000 08:01 2184471                            /root/memory_allocator/src/main/main
00a69000-00d9d000 rw-p 00668000 08:01 2184471                            /root/memory_allocator/src/main/main
00d9d000-00dcc000 rw-p 00000000 00:00 0
012ff000-01322000 rw-p 00000000 00:00 0                                  [heap]
c000000000-c000001000 rw-p 00000000 00:00 0
c41fff8000-c420100000 rw-p 00000000 00:00 0
7f94b4000000-7f94b4021000 rw-p 00000000 00:00 0
7f94b4021000-7f94b8000000 ---p 00000000 00:00 0
7f94b934b000-7f94b9f8e000 rw-p 00000000 00:00 0
7f94b9f8e000-7f94b9f8f000 ---p 00000000 00:00 0
7f94b9f8f000-7f94bac02000 rw-p 00000000 00:00 0
7ffed6128000-7ffed6149000 rw-p 00000000 00:00 0                          [stack]
7ffed61d2000-7ffed61d5000 r--p 00000000 00:00 0                          [vvar]
7ffed61d5000-7ffed61d7000 r-xp 00000000 00:00 0                          [vdso]
```
