package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
)

// currenciesSeed is generated from SIX ISO 4217 List One, the official
// maintenance-agency source. Exchange rates are not embedded: a separate,
// best-effort ECB bootstrap downloads recent public reference observations.
//
//go:embed currencies_seed.json
var currenciesSeed []byte

type currencySeedFile struct {
	Source     string               `json:"source"`
	Published  string               `json:"published"`
	Currencies []CurrencyDefinition `json:"currencies"`
}

func seedCurrencyDefinitions(db *sql.DB) error {
	var seed currencySeedFile
	if err := json.Unmarshal(currenciesSeed, &seed); err != nil {
		return fmt.Errorf("decode currency seed: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO currency_definitions
        (code,numeric_code,name,minor_units,kind,active,data_version,updated_at)
        VALUES (?,?,?,?,?,1,?,CURRENT_TIMESTAMP)
        ON CONFLICT(code) DO UPDATE SET numeric_code=excluded.numeric_code,
          name=excluded.name, minor_units=excluded.minor_units,
          kind=excluded.kind, active=1, data_version=excluded.data_version,
          updated_at=CURRENT_TIMESTAMP`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range seed.Currencies {
		var minor any
		if c.MinorUnits != nil {
			minor = *c.MinorUnits
		}
		if _, err := stmt.Exec(c.Code, c.NumericCode, c.Name, minor, c.Kind, seed.Published); err != nil {
			return fmt.Errorf("seed %s: %w", c.Code, err)
		}
	}
	return tx.Commit()
}
