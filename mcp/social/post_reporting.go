package main

import (
	"fmt"
	"strconv"
)

// Counts use the same target/account join as the returned targets array.
// SUM preserves one post per row, including drafts without any targets.
// Callers append a parameterized FROM posts WHERE clause, before pagination.
const postCountsSQL = `SELECT COUNT(*), COALESCE(SUM((
	SELECT COUNT(*) FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id
	WHERE t.post_id=posts.id
)),0)`

func postListOffset(args map[string]any) (int, error) {
	raw, present := args["offset"]
	if !present || raw == nil {
		return 0, nil
	}
	value := fmt.Sprint(raw)
	if n, ok := raw.(float64); ok {
		value = strconv.FormatFloat(n, 'f', -1, 64)
	}
	n, err := strconv.ParseInt(value, 10, 0)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("offset must be a non-negative integer")
	}
	return int(n), nil
}
