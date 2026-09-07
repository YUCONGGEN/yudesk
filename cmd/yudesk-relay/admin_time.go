package main

import "time"

// The deployed host uses PDT. Product administration uses explicit Beijing
// time, independent of host environment or installed zoneinfo files.
var adminTimeZone = time.FixedZone("UTC+08:00", 8*60*60)

func formatAdminTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.In(adminTimeZone).Format("2006-01-02 15:04:05")
}

func adminLastSeen(stored time.Time, control *deviceControl) time.Time {
	if control != nil {
		if unix := control.lastSeen.Load(); unix > 0 {
			if live := time.Unix(unix, 0); live.After(stored) {
				return live
			}
		}
	}
	return stored
}
