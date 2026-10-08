package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"
)

// Embed the static directory into the Go binary
//
//go:embed static/*
var staticFS embed.FS

type Database struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	StorageGB int    `json:"storage_gb"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	OwnerID   string `json:"owner_id"`
}

type Datastore struct {
	mu        sync.RWMutex
	databases map[string]*Database
}

var store = &Datastore{
	databases: map[string]*Database{
		"db-001": {
			ID:        "db-001",
			Name:      "prod-users-db",
			Engine:    "postgres",
			StorageGB: 50,
			Status:    "ready",
			CreatedAt: time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			OwnerID:   "org-engineering-01",
		},
	},
}

func main() {
	mux := http.NewServeMux()

	// 2. Embedded Static UI Routes
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(subFS)))

	// 3. Start HTTP Server
	port := ":8082"
	fmt.Printf("🚀 TigerData Server running on http://localhost%s\n", port)
	log.Fatal(http.ListenAndServe(port, corsMiddleware(mux)))
}

// Global CORS Middleware for dev flexibility
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Owner-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GET /v1/databases & POST /v1/databases
func handleDatabases(w http.ResponseWriter, r *http.Request) {
	ownerID := r.Header.Get("X-Owner-ID")
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		store.mu.RLock()
		defer store.mu.RUnlock()

		list := make([]*Database, 0)
		for _, db := range store.databases {
			if ownerID == "" || db.OwnerID == ownerID {
				list = append(list, db)
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"databases": list})

	case http.MethodPost:
		var req struct {
			Name      string `json:"name"`
			Engine    string `json:"engine"`
			StorageGB int    `json:"storage_gb"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		newID := fmt.Sprintf("db-%d", time.Now().UnixNano()%100000)
		newDB := &Database{
			ID:        newID,
			Name:      req.Name,
			Engine:    req.Engine,
			StorageGB: req.StorageGB,
			Status:    "ready",
			CreatedAt: time.Now().Format(time.RFC3339),
			OwnerID:   ownerID,
		}
		store.databases[newID] = newDB
		store.mu.Unlock()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newDB)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// PATCH /v1/databases/{id} & DELETE /v1/databases/{id}
func handleDatabaseByID(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/v1/databases/"):]
	w.Header().Set("Content-Type", "application/json")

	store.mu.Lock()
	db, exists := store.databases[id]
	if !exists {
		store.mu.Unlock()
		http.Error(w, `{"error":"database not found"}`, http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodPatch:
		var req struct {
			StorageGB int `json:"storage_gb"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			store.mu.Unlock()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.StorageGB > 0 {
			db.StorageGB = req.StorageGB
		}
		store.mu.Unlock()
		json.NewEncoder(w).Encode(db)

	case http.MethodDelete:
		delete(store.databases, id)
		store.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		store.mu.Unlock()
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
