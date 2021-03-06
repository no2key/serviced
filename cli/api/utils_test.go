// Copyright 2015 The Serviced Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build unit

package api

import (
	. "gopkg.in/check.v1"
)

func (s *TestAPISuite) TestEnvironConfigReader_StringMap_Empty(c *C) {
	listValue := []string{""}
	expected := map[string]string{}

	result := convertStringSliceToMap(listValue)

	c.Assert(result, DeepEquals, expected)
}

func (s *TestAPISuite) TestEnvironConfigReader_StringMap_Simple(c *C) {
	listValue := []string{"keySimple=valueSimple"}
	expected := map[string]string{"keySimple": "valueSimple"}

	result := convertStringSliceToMap(listValue)

	c.Assert(result, DeepEquals, expected)
}

func (s *TestAPISuite) TestEnvironConfigReader_StringMap_Multiple(c *C) {
	listValue := []string{"key1=value1", "key2=value2", "key3=value3"}
	expected := map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"}

	result := convertStringSliceToMap(listValue)

	c.Assert(result, DeepEquals, expected)
}

func (s *TestAPISuite) TestEnvironConfigReader_StringMap_EmptyPair(c *C) {
	listValue := []string{"key1=value1", "", "key2=value2", ""}
	expected := map[string]string{"key1": "value1", "key2": "value2"}

	result := convertStringSliceToMap(listValue)

	c.Assert(result, DeepEquals, expected)
}

func (s *TestAPISuite) TestEnvironConfigReader_StringMap_InvalidPair(c *C) {
	listValue := []string{"key1=value1", "key2=", "=value3", "foo"}
	expected := map[string]string{"key1": "value1", "key2": ""}

	result := convertStringSliceToMap(listValue)

	c.Assert(result, DeepEquals, expected)
}
