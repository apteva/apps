-- pantry v0.3: shopping rows should reference item definitions.
--
-- Earlier versions allowed manual shopping rows without item_id. Create
-- lightweight item definitions for those rows, then link them.

INSERT OR IGNORE INTO items (project_id, name, category, default_unit)
SELECT project_id, name, category, unit
  FROM shopping_list_items
 WHERE item_id IS NULL
   AND name != '';

UPDATE shopping_list_items
   SET item_id = (
       SELECT items.id
         FROM items
        WHERE items.project_id = shopping_list_items.project_id
          AND lower(items.name) = lower(shopping_list_items.name)
        LIMIT 1
   )
 WHERE item_id IS NULL;
