// Open-Meteo wire contract: URL builders, response documents, and the
// WMO weather-code tables. Both hosts must appear in the operator
// policy's network.allowed_hosts for anything to load — geocoding-api
// resolves place names, api serves the forecast.
package weather

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	geocodeHost  = "geocoding-api.open-meteo.com"
	forecastHost = "api.open-meteo.com"
)

// geocodeURL builds the geocoding lookup for a free-form place name. The
// result count is bounded so a snapshot never carries more candidates
// than it can display.
func geocodeURL(query string) string {
	q := url.Values{
		"name":     {query},
		"count":    {strconv.Itoa(maxSearchResults)},
		"language": {"en"},
		"format":   {"json"},
	}
	return "https://" + geocodeHost + "/v1/search?" + q.Encode()
}

// forecastURL builds the forecast request for one location: current
// conditions plus a bounded daily outlook in the location's own timezone.
func forecastURL(loc Location) string {
	q := url.Values{
		"latitude":      {strconv.FormatFloat(loc.Latitude, 'f', -1, 64)},
		"longitude":     {strconv.FormatFloat(loc.Longitude, 'f', -1, 64)},
		"current":       {"temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,weather_code,wind_speed_10m,wind_direction_10m,is_day"},
		"daily":         {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max"},
		"timezone":      {"auto"},
		"forecast_days": {strconv.Itoa(maxForecastDays)},
	}
	return "https://" + forecastHost + "/v1/forecast?" + q.Encode()
}

// geocodeReply is the Open-Meteo geocoding document. An absent results
// array is the honest "not found"; error+reason is the API's own report.
type geocodeReply struct {
	Results []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Country   string  `json:"country"`
		Admin1    string  `json:"admin1"`
		Timezone  string  `json:"timezone"`
	} `json:"results"`
	Error  bool   `json:"error"`
	Reason string `json:"reason"`
}

// parseGeocode decodes a geocoding body into bounded candidate locations
// with cand_N item ids. Zero matches is not an error — the caller renders
// the honest "no places match" guidance.
func parseGeocode(data []byte) ([]Location, error) {
	var g geocodeReply
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("the geocoding response was not readable")
	}
	if g.Error {
		return nil, fmt.Errorf("the geocoding service reported: %s", apiReason(g.Reason))
	}
	cands := make([]Location, 0, len(g.Results))
	for _, r := range g.Results {
		if len(cands) >= maxSearchResults {
			break
		}
		name := sanitizeLine(r.Name)
		if name == "" || math.IsNaN(r.Latitude) || math.IsNaN(r.Longitude) ||
			r.Latitude < -90 || r.Latitude > 90 || r.Longitude < -180 || r.Longitude > 180 {
			continue
		}
		cands = append(cands, Location{
			ID:        fmt.Sprintf("cand_%d", len(cands)),
			Name:      name,
			Region:    sanitizeLine(r.Admin1),
			Country:   sanitizeLine(r.Country),
			Latitude:  r.Latitude,
			Longitude: r.Longitude,
			Timezone:  sanitizeLine(r.Timezone),
		})
	}
	return cands, nil
}

// forecastReply is the Open-Meteo forecast document. Fields are pointers
// because the API emits null for missing observations — a nil is honest
// "no reading", never a fabricated zero.
type forecastReply struct {
	Error   bool   `json:"error"`
	Reason  string `json:"reason"`
	Current struct {
		Time      string   `json:"time"`
		Temp      *float64 `json:"temperature_2m"`
		Feels     *float64 `json:"apparent_temperature"`
		Humidity  *float64 `json:"relative_humidity_2m"`
		Precip    *float64 `json:"precipitation"`
		Code      *int     `json:"weather_code"`
		WindSpeed *float64 `json:"wind_speed_10m"`
		WindDir   *float64 `json:"wind_direction_10m"`
		IsDay     *int     `json:"is_day"`
	} `json:"current"`
	Daily struct {
		Time      []string   `json:"time"`
		Code      []*int     `json:"weather_code"`
		TempMax   []*float64 `json:"temperature_2m_max"`
		TempMin   []*float64 `json:"temperature_2m_min"`
		PrecipMax []*float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// parseForecast decodes a forecast body into the cached snapshot. Days
// are zipped from the API's parallel arrays and capped at maxForecastDays.
func parseForecast(data []byte, fetchedAt time.Time) (Snapshot, error) {
	var f forecastReply
	if err := json.Unmarshal(data, &f); err != nil {
		return Snapshot{}, fmt.Errorf("the forecast response was not readable")
	}
	if f.Error {
		return Snapshot{}, fmt.Errorf("the weather service reported: %s", apiReason(f.Reason))
	}
	if f.Current.Temp == nil {
		return Snapshot{}, fmt.Errorf("the forecast carried no current conditions")
	}
	snap := Snapshot{
		FetchedAt:  fetchedAt,
		ObservedAt: f.Current.Time,
		TempC:      *f.Current.Temp,
		FeelsC:     *f.Current.Temp,
		Humidity:   deref(f.Current.Humidity),
		PrecipMM:   deref(f.Current.Precip),
		WindKph:    deref(f.Current.WindSpeed),
		WindDirDeg: deref(f.Current.WindDir),
		IsDay:      true,
	}
	if f.Current.Feels != nil {
		snap.FeelsC = *f.Current.Feels
	}
	if f.Current.Code != nil {
		snap.Code = *f.Current.Code
	}
	if f.Current.IsDay != nil {
		snap.IsDay = *f.Current.IsDay != 0
	}
	for i, date := range f.Daily.Time {
		if len(snap.Days) >= maxForecastDays {
			break
		}
		day := Day{Date: date}
		if v := pickInt(f.Daily.Code, i); v != nil {
			day.Code = *v
		}
		if v := pickFloat(f.Daily.TempMax, i); v != nil {
			day.MaxC = *v
		}
		if v := pickFloat(f.Daily.TempMin, i); v != nil {
			day.MinC = *v
		}
		day.PrecipPct = pickFloat(f.Daily.PrecipMax, i)
		snap.Days = append(snap.Days, day)
	}
	return snap, nil
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func pickInt(s []*int, i int) *int {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func pickFloat(s []*float64, i int) *float64 {
	if i < len(s) {
		return s[i]
	}
	return nil
}

func apiReason(reason string) string {
	reason = sanitizeLine(reason)
	if reason == "" {
		return "request rejected"
	}
	return reason
}

// describeCode maps a WMO weather interpretation code to its short
// lowercase label for status lines and item labels.
func describeCode(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1:
		return "mainly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75, 77:
		return "snow"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm with hail"
	default:
		return "unknown conditions"
	}
}

// compass renders a degree bearing as a 16-point compass direction.
func compass(deg float64) string {
	dirs := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
		"S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
	idx := int(math.Mod(deg+11.25, 360)/22.5) % 16
	return dirs[idx]
}

// shortDate renders the API's "2006-01-02" day key as "Thu Oct 8"; an
// unparseable date is shown verbatim rather than dropped.
func shortDate(date string) string {
	if t, err := time.Parse("2006-01-02", date); err == nil {
		return t.Format("Mon Jan 2")
	}
	return date
}

// sanitizeLine trims a remote-supplied string to one clean line; remote
// text is untrusted and must not smuggle terminal controls into items.
func sanitizeLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > maxLabelLen {
		s = strings.TrimSpace(s[:maxLabelLen])
	}
	return s
}

// --- version-2 art: one small glyph per condition family ---

const artClearDay = `   \   |   /
     .-.
―   (   )   ―
     '-'
   /   |   \`

const artClearNight = `      .   *
   ,--.      .
  (     ) *
   '--'      .`

const artPartly = `   \  |  /
     .-.     .--.
―   (   ) .-(    )
     '-'(__(____)`

const artCloud = `      .--.
   .-(    ).
  (___.__)__)`

const artFog = `  ───────────
    ───────────
  ───────────
    ───────────`

const artRain = `      .--.
   .-(    ).
  (___.__)__)
    ' ' ' '
   ' ' ' '`

const artSnow = `      .--.
   .-(    ).
  (___.__)__)
    * * * *
   * * * *`

const artStorm = `      .--.
   .-(    ).
  (___.__)__)
     / /
    / /`

// glyphFor picks the version-2 art for a condition code. Only clear sky
// distinguishes night; every other glyph reads the same in the dark.
func glyphFor(code int, isDay bool) string {
	switch code {
	case 0, 1:
		if isDay {
			return artClearDay
		}
		return artClearNight
	case 2:
		return artPartly
	case 3:
		return artCloud
	case 45, 48:
		return artFog
	case 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 80, 81, 82:
		return artRain
	case 71, 73, 75, 77, 85, 86:
		return artSnow
	case 95, 96, 99:
		return artStorm
	default:
		return artCloud
	}
}
