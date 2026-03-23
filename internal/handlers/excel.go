package handlers

import (
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
	"github.com/yourorg/whatsapp-broadcast/internal/models"
)

// headerMap maps common header variations to canonical field names
var headerMap = map[string]string{
	// Nama pasien
	"nama pasien": "name",
	"nama":        "name",
	"name":        "name",
	"patient name": "name",

	// No telp / phone
	"no telp":      "phone",
	"no. telp":     "phone",
	"no telpon":    "phone",
	"nomor telp":   "phone",
	"no hp":        "phone",
	"no. hp":       "phone",
	"nomor hp":     "phone",
	"phone":        "phone",
	"phone number": "phone",
	"whatsapp":     "phone",
	"wa":           "phone",
	"telp":         "phone",

	// Alamat
	"alamat":  "address",
	"address": "address",

	// HPHT (Hari Pertama Haid Terakhir — last menstrual period date)
	"hpht":                    "hpht",
	"tgl mens terakhir":       "hpht",
	"tanggal mens terakhir":   "hpht",
	"hari pertama haid terakhir": "hpht",
	"lmp":                     "hpht", // last menstrual period (English)

	// Hamil ke- (pregnancy number)
	"hamil ke":  "pregnancy_number",
	"hamil ke-": "pregnancy_number",
	"kehamilan": "pregnancy_number",
	"gravida":   "pregnancy_number",
}

// ParseExcel reads the first sheet of the uploaded file and returns patients.
func ParseExcel(path string, broadcastID uint) ([]models.Patient, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open excel: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("excel has no sheets")
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("get rows: %w", err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("excel must have at least a header row and one data row")
	}

	// Map canonical field name -> column index
	colIndex := map[string]int{}
	for i, h := range rows[0] {
		normalized := strings.ToLower(strings.TrimSpace(h))
		if canonical, ok := headerMap[normalized]; ok {
			colIndex[canonical] = i
		}
	}

	if _, ok := colIndex["name"]; !ok {
		return nil, fmt.Errorf("kolom 'Nama Pasien' tidak ditemukan di Excel")
	}
	if _, ok := colIndex["phone"]; !ok {
		return nil, fmt.Errorf("kolom 'No Telp' tidak ditemukan di Excel")
	}

	var patients []models.Patient
	for _, row := range rows[1:] {
		get := func(field string) string {
			idx, ok := colIndex[field]
			if !ok || idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}

		name := get("name")
		phone := sanitizePhone(get("phone"))
		if name == "" || phone == "" {
			continue // skip empty rows
		}

		patients = append(patients, models.Patient{
			BroadcastID:      broadcastID,
			Name:             name,
			Phone:            phone,
			Address:          get("address"),
			HPHT:             get("hpht"),
			PregnancyNumber:  get("pregnancy_number"),
		})
	}

	return patients, nil
}

// sanitizePhone strips non-numeric chars and normalises to Indonesian country code.
func sanitizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	// Local format 08xxx → 628xxx
	if strings.HasPrefix(s, "0") {
		s = "62" + s[1:]
	}
	return s
}