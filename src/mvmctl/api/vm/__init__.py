"""VM API module - public surface.

This module re-exports from api/vms.py for backward compatibility.
The api/vm/ package contains extracted components that will
replace this module in later phases.
"""

from __future__ import annotations

from mvmctl.api import vms

__all__ = vms.__all__

globals().update({name: getattr(vms, name) for name in __all__})
