package firewall

import (
	"bufio"
	"crypto/sha256"
)

const generationApplicationSealSchema = "mvmctl.firewall.application.v1"

// bindGenerationApplicationSeal records an internal integrity invariant over the exact compiled output. It detects
// accidental mutation between compilation and application; deletion authority still comes only from the retained
// generation lease, its admitted applying record, and a fresh exact qualified kernel observation.
func bindGenerationApplicationSeal(generation *compiledGeneration) error {
	seal, err := deriveGenerationApplicationSeal(*generation)
	if err != nil {
		return err
	}
	generation.applicationSeal = seal
	return nil
}

func deriveGenerationApplicationSeal(generation compiledGeneration) ([sha256.Size]byte, error) {
	digest := sha256.New()
	encoder := generationDigestEncoder{writer: bufio.NewWriterSize(digest, generationDigestBufferBytes)}
	encoder.text("schema", generationApplicationSealSchema)
	encoder.text("generation-id", generation.id)
	encoder.boolean("generation.empty", generation.empty)
	encoder.count("authority.endpoints", len(generation.endpointIDs))
	for _, identity := range generation.endpointIDs {
		encoder.text("authority.endpoint", identity)
	}
	encoder.count("authority.networks", len(generation.networkIDs))
	for _, identity := range generation.networkIDs {
		encoder.text("authority.network", identity)
	}
	if err := encodeCanonicalOutput(&encoder, generation); err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := encoder.writer.Flush(); err != nil {
		return [sha256.Size]byte{}, invalidGeneration("cannot finalize kernel application seal")
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}
