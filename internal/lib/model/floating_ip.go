package model

// FloatingIPItem is a display projection of root-owned floating-IP authority.
// It is not a SQLite ownership record or permission to configure a guest address.
type FloatingIPItem struct {
	ID         string `json:"id" db:"id"`
	NetworkID  string `json:"network_id" db:"network_id"`
	Name       string `json:"name" db:"name"`
	IPv4       string `json:"ipv4" db:"ipv4"`
	Generation uint64 `json:"generation" db:"generation"`
	Retired    bool   `json:"retired" db:"retired"`
}
