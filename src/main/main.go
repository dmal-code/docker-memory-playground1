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
	fmt.Println("called createEntities")
	params := mux.Vars(r)
 
  //clear the array
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
