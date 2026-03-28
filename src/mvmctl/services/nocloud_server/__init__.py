"""NoCloud HTTP server package for cloud-init datasource.

This package provides subprocess-based NoCloud server functionality
for serving cloud-init files to VMs via the nocloud-net datasource.
"""

from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

__all__ = ["NoCloudNetServerManager"]
