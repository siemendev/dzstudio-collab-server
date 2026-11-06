package protocol

import (
	"regexp"
	"strconv"
)

var clientVersionRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$`)

// ClientVersion is a client version MAJOR.MINOR.PATCH, each part 0–999 without leading zeros.
type ClientVersion [3]int

// ParseClientVersion reads a version; anything else (a suffix, two parts, leading zeros) is not one.
func ParseClientVersion(s string) (ClientVersion, bool) {
	m := clientVersionRE.FindStringSubmatch(s)
	if m == nil {
		return ClientVersion{}, false
	}
	var v ClientVersion
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// Less compares part by part as numbers: 0.9.3 is older than 0.10.0.
func (v ClientVersion) Less(o ClientVersion) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

func (v ClientVersion) String() string {
	return strconv.Itoa(v[0]) + "." + strconv.Itoa(v[1]) + "." + strconv.Itoa(v[2])
}
