module github.com/excalibase/excalibase-vault

go 1.25.0

require (
	github.com/excalibase/provisioning-poc v0.0.0
	github.com/go-chi/chi/v5 v5.2.5
	github.com/lib/pq v1.10.9
)

require (
	github.com/hashicorp/vault v1.15.4 // indirect
	go.etcd.io/bbolt v1.4.3 // indirect
	golang.org/x/sys v0.42.0 // indirect
)

replace github.com/excalibase/provisioning-poc => ../server-go
