"""Root partition detection using weighted detector heuristics."""

from typing import Protocol

from mvmctl.constants import DETECTOR_SCORES, DETECTOR_WEIGHTS, MIN_ROOT_SIZE_MB  # noqa: F401
from mvmctl.exceptions import RootPartitionDetectionError, TieDetectedError  # noqa: F401


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

    def score(self, partition: dict[str, object], all_partitions: list[dict[str, object]]) -> float:
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

    def __init__(self) -> None:
        """Initialize the detector with an empty registry."""
        self._detectors: list[PartitionDetector] = []

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
        raise NotImplementedError


class TypeCodeDetector:
    """Detector for identifying root partitions based on partition type codes.

    Linux root partitions typically have specific type codes that indicate
    their purpose in the system.
    """

    @property
    def name(self) -> str:
        return "type_code"

    @property
    def weight(self) -> float:
        return DETECTOR_WEIGHTS.get("type_code", 0.25)

    def score(self, partition: dict[str, object], all_partitions: list[dict[str, object]]) -> float:
        """Score a partition based on its type code.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on type code matching root filesystem patterns.
        """
        raise NotImplementedError


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
        return DETECTOR_WEIGHTS.get("label", 0.25)

    def score(self, partition: dict[str, object], all_partitions: list[dict[str, object]]) -> float:
        """Score a partition based on its filesystem label.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on label matching root filesystem patterns.
        """
        raise NotImplementedError


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
        return DETECTOR_WEIGHTS.get("size", 0.25)

    def score(self, partition: dict[str, object], all_partitions: list[dict[str, object]]) -> float:
        """Score a partition based on its size relative to minimum root size.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on size being >= MIN_ROOT_SIZE_MB threshold.
        """
        raise NotImplementedError


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
        return DETECTOR_WEIGHTS.get("filesystem", 0.25)

    def score(self, partition: dict[str, object], all_partitions: list[dict[str, object]]) -> float:
        """Score a partition based on its filesystem type.

        Args:
            partition: Partition information dictionary.
            all_partitions: All partitions for context.

        Returns:
            Score based on filesystem type matching common root types.
        """
        raise NotImplementedError
