package main

func seedSystemAttributes(db sqlQueryExecer, pid string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO contact_attribute_defs (project_id,key,label,type,enum_values,sort_order,is_system) SELECT ?,key,label,type,enum_values,sort_order,1 FROM _system_attribute_templates`, pid)
	return err
}

func lastContactID(ids []int64) int64 {
	if len(ids) == 0 {
		return 0
	}
	return ids[len(ids)-1]
}
