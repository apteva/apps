package main

import "testing"

func TestOutputCardsKeepCanonicalCompositionAndProjectScope(t *testing.T) {
	ctx := newPersonaCtx(t)
	for _, query := range []string{
		`INSERT INTO personas(id,project_id,name) VALUES(10,'a','Artist'),(11,'b','Artist')`,
		`INSERT INTO persona_compositions(project_id,persona_id,title,composer_composition_id) VALUES('a',10,'Song',72),('b',11,'Song',73)`,
	} {
		if _, err := ctx.AppDB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := listPersonaCompositions(ctx.AppDB(), "a", 10, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ComposerCompositionID != 72 {
		t.Fatalf("canonical composition missing: %+v", rows)
	}
	rows, err = listPersonaCompositions(ctx.AppDB(), "b", 10, 50)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cross-project composition exposed: %+v %v", rows, err)
	}
}
