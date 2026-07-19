package operations

import (
	"os"
	"strconv"
	"strings"
)

// processRSSBytes returns the Linux process resident set size for status reporting.
func processRSSBytes() int64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "VmRSS:" || fields[2] != "kB" {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && value >= 0 {
			return value * 1024
		}
		return 0
	}
	return 0
}
