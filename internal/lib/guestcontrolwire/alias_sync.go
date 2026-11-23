package guestcontrolwire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
)

const FloatingIPAliasPageSize = 32

// FloatingIPAlias is host-derived operator ownership for this authenticated incarnation.
// It contains no interface selector or executable guest operation.
type FloatingIPAlias struct {
	ResourceID string `json:"resource_id"`
	IPv4       string `json:"ipv4"`
	Generation uint64 `json:"generation"`
}

type FloatingIPAliasSnapshot struct {
	ID      string            `json:"id"`
	Aliases []FloatingIPAlias `json:"aliases"`
}

func (snapshot FloatingIPAliasSnapshot) Validate() error {
	if !requestIDPattern.MatchString(snapshot.ID) || snapshot.Aliases == nil {
		return invalid("invalid bounded operator alias snapshot")
	}
	previous := ""
	addresses := make(map[string]struct{}, len(snapshot.Aliases))
	for _, alias := range snapshot.Aliases {
		address, err := netip.ParseAddr(alias.IPv4)
		if !resourceIDPattern.MatchString(alias.ResourceID) || alias.ResourceID <= previous || alias.Generation == 0 ||
			err != nil || !address.Is4() || !address.IsPrivate() || address.String() != alias.IPv4 {
			return invalid("invalid ordered operator alias identity")
		}
		if _, duplicate := addresses[alias.IPv4]; duplicate {
			return invalid("duplicate operator alias address")
		}
		addresses[alias.IPv4] = struct{}{}
		previous = alias.ResourceID
	}
	return nil
}

func (snapshot FloatingIPAliasSnapshot) Digest() (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(snapshot.Aliases)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

// A page has no effects until every page and the complete digest have been admitted.
type FloatingIPAliasPage struct {
	SchemaVersion uint16            `json:"schema_version"`
	Feature       string            `json:"feature"`
	SessionID     string            `json:"session_id"`
	SnapshotID    string            `json:"snapshot_id"`
	Index         uint32            `json:"index"`
	Final         bool              `json:"final"`
	Digest        string            `json:"digest"`
	Aliases       []FloatingIPAlias `json:"aliases"`
}

type FloatingIPAliasAck struct {
	SchemaVersion uint16 `json:"schema_version"`
	Feature       string `json:"feature"`
	SessionID     string `json:"session_id"`
	SnapshotID    string `json:"snapshot_id"`
	Digest        string `json:"digest"`
	Accepted      bool   `json:"accepted"`
}

func validAliasSyncIdentity(session, snapshot, digest string) bool {
	raw, err := hex.DecodeString(digest)
	return requestIDPattern.MatchString(session) && requestIDPattern.MatchString(snapshot) && err == nil &&
		len(raw) == sha256.Size && hex.EncodeToString(raw) == digest
}

func (page FloatingIPAliasPage) Validate() error {
	if page.SchemaVersion != 1 || page.Feature != "floating_ip_alias_sync" ||
		!validAliasSyncIdentity(
			page.SessionID,
			page.SnapshotID,
			page.Digest,
		) || page.Aliases == nil || len(page.Aliases) > FloatingIPAliasPageSize || !page.Final && len(page.Aliases) != FloatingIPAliasPageSize {
		return invalid("invalid operator alias snapshot page")
	}
	return (FloatingIPAliasSnapshot{ID: page.SnapshotID, Aliases: page.Aliases}).Validate()
}
func (ack FloatingIPAliasAck) Validate() error {
	if ack.SchemaVersion != 1 || ack.Feature != "floating_ip_alias_ack" ||
		!validAliasSyncIdentity(ack.SessionID, ack.SnapshotID, ack.Digest) {
		return invalid("invalid operator alias snapshot acknowledgement")
	}
	return nil
}
func EncodeFloatingIPAliasPage(ctx context.Context, w io.Writer, page FloatingIPAliasPage) error {
	if err := page.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(page)
	if err != nil {
		return err
	}
	return writeFrame(ctx, w, body)
}
func EncodeFloatingIPAliasAck(ctx context.Context, w io.Writer, ack FloatingIPAliasAck) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	return writeFrame(ctx, w, body)
}

// DecodeHostMessage is the agent's sole stream reader. Only an outstanding guest request admits a response.
func DecodeHostMessage(ctx context.Context, r io.Reader, pending *Request) (*Response, *FloatingIPAliasPage, error) {
	body, err := readFrame(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	var feature struct {
		Feature string `json:"feature"`
	}
	if err = json.Unmarshal(body, &feature); err != nil {
		return nil, nil, err
	}
	if feature.Feature == "floating_ip_alias_sync" {
		fields, err := closedObject(
			body,
			[]string{"schema_version", "feature", "session_id", "snapshot_id", "index", "final", "digest", "aliases"},
		)
		if err != nil {
			return nil, nil, err
		}
		var aliases []json.RawMessage
		if err = json.Unmarshal(fields["aliases"], &aliases); err != nil {
			return nil, nil, err
		}
		for _, alias := range aliases {
			if _, err = closedObject(alias, []string{"resource_id", "ipv4", "generation"}); err != nil {
				return nil, nil, err
			}
		}
		var page FloatingIPAliasPage
		if err = json.Unmarshal(body, &page); err != nil {
			return nil, nil, err
		}
		if err = page.Validate(); err != nil {
			return nil, nil, err
		}
		return nil, &page, nil
	}
	if pending == nil {
		return nil, nil, invalid("unsolicited Guest Control response")
	}
	var frame bytes.Buffer
	if err = writeFrame(ctx, &frame, body); err != nil {
		return nil, nil, err
	}
	response, err := DecodeResponse(ctx, &frame, *pending)
	if err != nil {
		return nil, nil, err
	}
	return &response, nil, nil
}

// DecodeGuestMessage is the host's sole stream reader; sync acknowledgements are never guest feature requests.
func DecodeGuestMessage(ctx context.Context, r io.Reader) (*Request, *FloatingIPAliasAck, error) {
	body, err := readFrame(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	var feature struct {
		Feature string `json:"feature"`
	}
	if err = json.Unmarshal(body, &feature); err != nil {
		return nil, nil, err
	}
	if feature.Feature == "floating_ip_alias_ack" {
		if _, err = closedObject(body, []string{"schema_version", "feature", "session_id", "snapshot_id", "digest", "accepted"}); err != nil {
			return nil, nil, err
		}
		var ack FloatingIPAliasAck
		if err = json.Unmarshal(body, &ack); err != nil {
			return nil, nil, err
		}
		if err = ack.Validate(); err != nil {
			return nil, nil, err
		}
		return nil, &ack, nil
	}
	var frame bytes.Buffer
	if err = writeFrame(ctx, &frame, body); err != nil {
		return nil, nil, err
	}
	request, err := DecodeRequest(ctx, &frame)
	if err != nil {
		return nil, nil, err
	}
	return &request, nil, nil
}
