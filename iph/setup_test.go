package main

import "testing"

func TestParsePhonesKeepsOnlyPairedPhysicalPhones(t *testing.T) {
	b := []byte(`{"result":{"devices":[
	 {"identifier":"11111111-2222-3333-4444-555555555555","hardwareProperties":{"deviceType":"iPhone","reality":"physical"},"connectionProperties":{"pairingState":"paired"},"deviceProperties":{"name":"Phone"}},
	 {"identifier":"A","hardwareProperties":{"deviceType":"iPhone","reality":"simulated"},"connectionProperties":{"pairingState":"paired"},"deviceProperties":{"name":"sim"}},
	 {"identifier":"B","hardwareProperties":{"deviceType":"iPad","reality":"physical"},"connectionProperties":{"pairingState":"paired"},"deviceProperties":{"name":"iPad"}},
	 {"identifier":"C","hardwareProperties":{"deviceType":"iPhone","reality":"physical"},"connectionProperties":{"pairingState":"unpaired"},"deviceProperties":{"name":"new"}}]}}`)
	ps, err := parsePhones(b)
	if err != nil || len(ps) != 1 || ps[0].name != "Phone" {
		t.Fatalf("parsePhones = %v, %v; want only the paired physical iPhone", ps, err)
	}
}
