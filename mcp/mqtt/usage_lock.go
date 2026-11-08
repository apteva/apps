package main

import (
	"database/sql"
	"fmt"
	"os"
)

// File locks are automatically released on crash, unlike persisted socket
// reservations. A second broker must not clear a live broker's reservations.
func lockUsageDatabase(db *sql.DB) (func(), error) {
	rows, err := db.Query(`PRAGMA database_list`)
	if err != nil {
		return nil, err
	}
	filename := ""
	for rows.Next() {
		var seq int
		var name, path string
		if err = rows.Scan(&seq, &name, &path); err != nil {
			rows.Close()
			return nil, err
		}
		if name == "main" {
			filename = path
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if filename == "" {
		return func() {}, nil
	} // test-only in-memory databases
	f, err := os.OpenFile(filename+".mqtt.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockBrokerFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("MQTT database already owned or cannot be locked: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
