//  Copyright (c) 2014 Couchbase, Inc.
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the
//  License. You may obtain a copy of the License at
//    http://www.apache.org/licenses/LICENSE-2.0
//  Unless required by applicable law or agreed to in writing,
//  software distributed under the License is distributed on an "AS
//  IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
//  express or implied. See the License for the specific language
//  governing permissions and limitations under the License.

package cbft

import (
	"testing"
)

func TestVersionGTE(t *testing.T) {
	tests := []struct {
		x        string
		y        string
		expected bool
	}{
		{"0.0.0", "0.0.0", true},
		{"0.0.1", "0.0.0", true},
		{"3.0.1", "2.0", true},
		{"3.0.0", "3.0", true},
		{"2.0.0", "2.0", true},
		{"2.0.1", "2.0", true},
		{"2.0.0", "2.5", false},
		{"1.0", "1.0.0", false},
		{"0.0", "0.0.0", false},
		{"", "", false},
		{"0", "", false},
		{"0.0", "", false},
		{"", "0", false},
		{"", "0.0", false},
		{"hello", "hello", false},
		{"0", "hello", false},
		{"0.0", "hello", false},
		{"hello", "0", false},
		{"hello", "0.0", false},
	}

	for i, test := range tests {
		actual := VersionGTE(test.x, test.y)
		if actual != test.expected {
			t.Errorf("test: %d, expected: %v, when %s >= %s, got: %v",
				i, test.expected, test.x, test.y, actual)
		}
	}
}

func TestNewUUID(t *testing.T) {
	u0 := NewUUID()
	u1 := NewUUID()
	if u0 == "" || u1 == "" || u0 == u1 {
		t.Errorf("NewUUID() failed, %s, %s", u0, u1)
	}
}

func TestExponentialBackoffLoop(t *testing.T) {
	called := 0
	ExponentialBackoffLoop("test", func() int {
		called += 1
		return -1
	}, 0, 0, 0)
	if called != 1 {
		t.Errorf("expected 1 call")
	}

	called = 0
	ExponentialBackoffLoop("test", func() int {
		called += 1
		if called <= 1 {
			return 1
		}
		return -1
	}, 0, 0, 0)
	if called != 2 {
		t.Errorf("expected 2 calls")
	}

	called = 0
	ExponentialBackoffLoop("test", func() int {
		called += 1
		if called == 1 {
			return 1
		}
		if called == 2 {
			return 0
		}
		return -1
	}, 0, 0, 0)
	if called != 3 {
		t.Errorf("expected 2 calls")
	}

	called = 0
	ExponentialBackoffLoop("test", func() int {
		called += 1
		if called == 1 {
			return 1
		}
		if called == 2 {
			return 0
		}
		return -1
	}, 1, 100000.0, 1)
	if called != 3 {
		t.Errorf("expected 2 calls")
	}
}
