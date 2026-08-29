package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestScalewayBucketNames(t *testing.T) {
	body := `<?xml version="1.0"?><ListAllMyBucketsResult><Buckets><Bucket><Name>alpha</Name></Bucket><Bucket><Name>beta</Name></Bucket></Buckets></ListAllMyBucketsResult>`
	encoded, _ := json.Marshal(body)
	if got, want := scalewayBucketNames(encoded), []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scalewayBucketNames()=%v, want %v", got, want)
	}
}
