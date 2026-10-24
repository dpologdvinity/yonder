// testutil_test.go holds small helpers shared by the test files.

package main

import "testing"

// must fails the test at once if err is not nil. Use it for test setup that has to work.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
