"""Root partition detection using weighted detector heuristics."""

import logging
from typing import Protocol

from mvmctl import constants
from mvmctl.exceptions import RootPartitionDetectionError, TieDetectedError  # noqa: F401

logger = logging.getLogger(__name__)


class PartitionDetector(Protocol):
    """Protocol defining the interface for partition detectors.

    Each detector evaluates partition characteristics and returns a score
    indicating how likely a partition is to be the root filesystem.
    """

    @property
    def name(self) -> str:
        """Detector identifier."""
        ...

    @property
    def weight(self) -> float:
        """Relative weight for this detector in the final score."""
        ...

    def score(
        self,
        partition: dict[str, object],
        all_partitions: list[dict[str, object]],
    ) -> float:
        """Evaluate a partition and return a score.

        Args:
            partition: Dictionary containing partition information (e.g., type code,
                label, size, filesystem type).
            all_partitions: List of all detected partitions for context comparison.

        Returns:
            Score between 0.0 and 1.0 indicating partition suitability as root.
        """
        ...


class RootPartitionDetector:
    """Detects the most likely root partition using weighted detector heuristics.

    Combines multiple detector strategies (type code, label, size, filesystem)
    to identify the most suitable candidate for use as the root filesystem.
    """

    def __init__(self, disabled_detectors: list[str] | None = None) -> None:
        """Initialize the detector with all built-in detectors registered.

        Args:
            disabled_detectors: List of detector names to skip during detection.
        """
        self._detectors: list[PartitionDetector] = [
            TypeCodeDetector(),
            LabelDetector(),
            SizeDetector(),
            FilesystemDetector(),
        ]
        self._disabled = set(disabled_detectors or [])

    def register(self, detector: PartitionDetector) -> None:
        """Register a detector for use in root partition detection.

        Args:
            detector: A PartitionDetector implementation to add to the registry.
        """
        self._detectors.append(detector)

    def detect(self, partitions: list[dict[str, object]]) -> int:
        """Detect the most likely root partition from a list of candidates.

        Args:
            partitions: List of partition dictionaries to evaluate.

        Returns:
            Index of the detected root partition in the input list.

        Raises:
            RootPartitionDetectionError: If no suitable root partition is found.
            TieDetectedError: If multiple partitions score equally and highest.
        """
        if len(partitions) == 1:
            return 1

        scores: list[tuple[int, float]] = []
        for i, partition in enumerate(partitions):
            total = sum(
                detector.weight * detector.score(partition, partitions)
                for detector in self._detectors
                if detector.name not in self._disabled
            )
            scores.append((i + 1, total))
            logger.debug("Partition %d score: %f", i + 1, total)

        best_score = max(score for _, score in scores)
        best_partitions = [p for p, s in scores if s == best_score]

        if len(best_partitions) > 1:
            raise TieDetectedError(
                [str(p) for p in best_partitions],
                partitions=partitions,
            )

        if best_score < 0:
            raise RootPartitionDetectionError(
                f"Best score {best_score} < 0, no suitable root partition found",
                partitions=partitions,
            )

        return best_partitions[0]


class TypeCodeDetector:
    """Detector for identifying root partitions based on partition type codes.

    Linux root partitions typically have specific type codes that indicate
    their purpose in the system.
    """

    # GPT type GUIDs
    GPT_ROOT_X86_64 = "44479540-f297-41b2-9af7-d131d5f0458a"
    GPT_ROOT_AARCH64 = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
    GPT_ESP = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
    GPT_SWAP = "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f"

    # MBR type codes (as strings from sfdisk JSON)
    MBR_LINUX = "83"
    MBR_EFI = "ef"
    MBR_SWAP = "82"
    MBR_EXTENDED = "85"
    MBR_LVM = "8e"

    @property
    def name(self) -> str:
        return "type_code"

    @property
    def weight(self) -> float:
        return constants.DETECTOR_WEIGHTS.get("type_code", 0.25)

    def score(
        self,
        partition: dict[str, object],
        all_partitions: list[dict[str, object]],
    ) -> float:
        """Score a partition based on its type code.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on type code matching root filesystem patterns.
        """
        partition_type = partition.get("type", "")
        if not isinstance(partition_type, str):
            return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)

        type_lower = partition_type.lower()

        # Root partitions get highest score
        if type_lower in (
            self.GPT_ROOT_X86_64.lower(),
            self.GPT_ROOT_AARCH64.lower(),
        ):
            return constants.DETECTOR_SCORES.get("ROOT_SCORE", 1.0)

        # Linux MBR type gets medium score
        if type_lower == self.MBR_LINUX.lower():
            return constants.DETECTOR_SCORES.get("MBR_LINUX_SCORE", 0.5)

        # Exclude partitions (ESP, swap, LVM, extended) get negative score
        if type_lower in (
            self.GPT_ESP.lower(),
            self.GPT_SWAP.lower(),
            self.MBR_EFI.lower(),
            self.MBR_SWAP.lower(),
            self.MBR_EXTENDED.lower(),
            self.MBR_LVM.lower(),
        ):
            return constants.DETECTOR_SCORES.get("EXCLUDE_SCORE", -1.0)

        # Unknown types get neutral score
        return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)


class LabelDetector:
    """Detector for identifying root partitions based on filesystem labels.

    Root partitions often have specific labels like 'ROOT', 'root', or
    variations that indicate their purpose.
    """

    @property
    def name(self) -> str:
        return "label"

    @property
    def weight(self) -> float:
        return constants.DETECTOR_WEIGHTS.get("label", 0.25)

    def score(
        self,
        partition: dict[str, object],
        all_partitions: list[dict[str, object]],
    ) -> float:
        """Score a partition based on its filesystem label.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on label matching root filesystem patterns.
        """
        # Get label from partition["name"] or partition["label"], default to empty string
        label = partition.get("name", "") or partition.get("label", "")
        if not isinstance(label, str):
            return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)

        label_lower = label.lower()

        # Root indicators - positive score
        root_indicators = ("root", "cloudimg", "rootfs")
        for indicator in root_indicators:
            if indicator in label_lower:
                return constants.DETECTOR_SCORES.get("LABEL_ROOT_SCORE", 1.0)

        # Exclude indicators - negative score
        exclude_indicators = ("esp", "efi", "boot", "swap")
        for indicator in exclude_indicators:
            if indicator in label_lower:
                return constants.DETECTOR_SCORES.get(
                    "LABEL_EXCLUDE_SCORE", -0.5
                )

        # No indicators - neutral score
        return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)


class SizeDetector:
    """Detector for identifying root partitions based on partition size.

    Root filesystems typically have size characteristics that distinguish
    them from small boot/EFI partitions.
    """

    @property
    def name(self) -> str:
        return "size"

    @property
    def weight(self) -> float:
        return constants.DETECTOR_WEIGHTS.get("size", 0.25)

    def score(
        self,
        partition: dict[str, object],
        all_partitions: list[dict[str, object]],
    ) -> float:
        """Score a partition based on its size relative to minimum root size.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on size being >= MIN_ROOT_SIZE_MB threshold.
        """
        # Get partition size from partition["size"] (in sectors)
        size_value = partition.get("size", 0)
        if not isinstance(size_value, (int, float)):
            return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)

        # Convert sectors to MB: size_mb = size_sectors * SECTOR_SIZE / MEBIBYTE
        sector_bytes = constants.CONST_SECTOR_SIZE_BYTES
        mebibyte_bytes = constants.CONST_MEBIBYTE_BYTES
        size_mb = float(size_value) * sector_bytes / mebibyte_bytes

        # Find the largest partition from all_partitions
        max_size_mb = float(0)
        for p in all_partitions:
            p_size = p.get("size", 0)
            if isinstance(p_size, (int, float)):
                p_size_mb = float(p_size) * sector_bytes / mebibyte_bytes
                if p_size_mb > max_size_mb:
                    max_size_mb = p_size_mb

        min_root_size_mb = constants.MIN_ROOT_SIZE_MB
        too_small_mb = constants.SIZE_TOO_SMALL_MB

        # Too small for root filesystem (< too_small_mb)
        if size_mb < too_small_mb:
            return constants.DETECTOR_SCORES.get("SIZE_TOO_SMALL_SCORE", -0.5)

        # At least MIN_ROOT_SIZE_MB (500MB)
        if size_mb >= min_root_size_mb:
            # Check if this is the largest partition
            if size_mb >= max_size_mb:
                return constants.DETECTOR_SCORES.get("SIZE_LARGEST_SCORE", 0.5)
            else:
                return constants.DETECTOR_SCORES.get("SIZE_ROOT_SCORE", 0.3)

        # Medium-sized partition (too_small_mb - min_root_size_mb, not largest)
        return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)


class FilesystemDetector:
    """Detector for identifying root partitions based on filesystem type.

    Root filesystems typically use common Linux filesystems like ext4,
    btrfs, or xfs.
    """

    @property
    def name(self) -> str:
        return "filesystem"

    @property
    def weight(self) -> float:
        return constants.DETECTOR_WEIGHTS.get("filesystem", 0.25)

    def score(
        self,
        partition: dict[str, object],
        all_partitions: list[dict[str, object]],
    ) -> float:
        """Score a partition based on its filesystem type.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on filesystem type matching common root types.
        """
        fstype = partition.get("fstype", "")
        if not isinstance(fstype, str):
            return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)

        fstype_lower = fstype.lower()
        root_filesystems = ("ext4", "btrfs", "xfs", "f2fs")

        if fstype_lower in root_filesystems:
            return constants.DETECTOR_SCORES.get("FILESYSTEM_ROOT_SCORE", 0.5)
        if fstype_lower == "vfat":
            return constants.DETECTOR_SCORES.get("FILESYSTEM_VFAT_SCORE", -0.8)
        if fstype_lower in ("crypto_luks", ""):
            return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)

        return constants.DETECTOR_SCORES.get("NEUTRAL_SCORE", 0.0)
