package model

import "strings"

// eventLabels are the human names for registry events, in Indonesian, used
// in Telegram messages. The dashboard carries the same list in
// js/core/format.js; keep both in step when adding events.
var eventLabels = map[string]string{
	"APP_STARTED": "Aplikasi dimulai", "APP_CRASHED": "Aplikasi crash", "APP_EXCEPTION": "Kesalahan kode (exception)",
	"PAYMENT_REJECTED": "Pembayaran ditolak", "PAYMENT_TIMEOUT": "Pembayaran timeout", "INSUFFICIENT_BALANCE": "Saldo tidak cukup",
	"QRIS_NOT_CONFIGURED": "QRIS belum dikonfigurasi",
	"READER_DISCONNECTED": "Pembaca kartu terputus", "CARD_READ_TIMEOUT": "Baca kartu timeout",
	"READER_NOT_READY": "Pembaca kartu tidak siap", "READER_INIT_FAILED": "Pembaca kartu gagal inisialisasi",
	"PRINTER_DISCONNECTED": "Printer terputus", "PRINT_FAILED": "Cetak gagal",
	"LPR_TIMEOUT": "Kamera LPR timeout", "LPR_NO_FEED": "Kamera LPR tidak mengirim gambar", "CCTV_DISCONNECTED": "CCTV terputus",
	"MQTT_DISCONNECTED": "MQTT terputus", "MQTT_CONNECT_FAILED": "MQTT gagal terhubung", "MQTT_DISABLED": "MQTT dimatikan",
	"NETWORK_DISCONNECTED": "Jaringan terputus", "HIGH_LATENCY": "Jaringan lambat",
	"HTTP_SERVER_ERROR": "Server pembayaran error", "HTTP_TIMEOUT": "Server pembayaran timeout", "HTTP_CLIENT_ERROR": "Permintaan ke server ditolak",
	"DATABASE_ERROR": "Database error", "DEVICE_ERROR": "Perangkat error",
	"GATE_OPEN_FAILED": "Palang tidak terbuka", "PCLESS_DISCONNECTED": "Palang (PCLess) terputus",
	"SYSTEM_ERROR": "Sistem error", "SENTINEL_GAP": "Kiosk sempat tidak terhubung ke Sentinel",
	StatusDownEvent: "Gate mati (tidak ada kabar)",
}

// EventLabel returns the human name for an event, the symbolic name when
// there is none, and "Log aplikasi: logger / fungsi" for catch-all events.
func EventLabel(name string) string {
	if l, ok := eventLabels[name]; ok {
		return l
	}
	if strings.HasPrefix(name, "LOG_") {
		parts := strings.SplitN(name[4:], "__", 2)
		src := strings.ToLower(strings.ReplaceAll(parts[0], "_", "."))
		if src == "" {
			src = "root"
		}
		if len(parts) == 2 && parts[1] != "" {
			return "Log aplikasi: " + src + " / " + strings.ToLower(parts[1])
		}
		return "Log aplikasi: " + src
	}
	return name
}
