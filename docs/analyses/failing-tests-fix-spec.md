# Failing tests fix spec

Summary of minimal fixes applied to make three failing tests pass:

1) tests/unit/test_config_state.py
- Updated assertion in test_get_assets_config_cache_dirs_under_cache so that keys_dir is expected under the cache directory (MVM_CACHE_DIR) instead of the config directory. This aligns the test with src/mvmctl/utils/fs.py:get_keys_dir() which returns get_cache_dir() / 'keys'.

2) tests/integration/test_console_integration.py
- Two TestConsoleWorkflow tests (create_vm_with_console_starts_relay and create_vm_without_console_skips_relay) were patched to mock mvmctl.core.vm_lifecycle.shutil.copy2 at the module level where vm_lifecycle uses it. The tests now include a @patch for shutil.copy2 and set mock_copy2.return_value = None to ensure MagicMock path objects don't reach the real shutil.copy2 during create_vm().

3) Documentation
- This spec file documents the above minimal, focused test-only fixes.
