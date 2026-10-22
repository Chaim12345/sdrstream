package bandcond

import "encoding/json"

// newestKp reads the Kp value of the row with the newest time_tag from the
// NOAA product (array of {"time_tag","Kp",...} objects, oldest first).
func newestKp(body []byte) (float64, bool) {
	var rows []struct {
		TimeTag string  `json:"time_tag"`
		Kp      float64 `json:"Kp"`
	}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
		return 0, false
	}
	return rows[len(rows)-1].Kp, true
}
