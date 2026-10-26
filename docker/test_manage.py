"""Config/migration safety tests; no Docker daemon or live state is touched."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("docker_manage_under_test", Path(__file__).with_name("manage.py"))
manage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manage)


def result(stdout="", code=0):
    return subprocess.CompletedProcess([], code, stdout, "")


class ConfigInitializationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.workspace = Path(self.temp.name)
        self.root = self.workspace / "sdn-controller"
        self.root.mkdir()
        self.config = self.root / "docker/state/config"
        for role, value in (
            ("agent", {"agent": {"listen": ":9090"}, "redis": {"enabled": True}}),
            ("controller", {"controller": {"listen": ":19090"}, "etcd": {"enabled": True}}),
        ):
            folder = self.workspace / ("sdn-"+role) / "docker"
            folder.mkdir(parents=True, exist_ok=True)
            (folder / (role+".conf")).write_text(json.dumps(value))
        self.patches = [patch.object(manage, "ROOT", self.root), patch.object(manage, "CONFIG", self.config),
                        patch.object(manage, "STATE", self.config.parent)]
        for p in self.patches:
            p.start()

    def tearDown(self):
        for p in reversed(self.patches):
            p.stop()
        self.temp.cleanup()

    def test_generates_node_specific_local_database_configs_without_tokens(self):
        self.assertEqual(len(manage.ensure_configs()), 4)
        controller = json.loads((self.config / "controller/controller.conf").read_text())
        self.assertEqual(controller["controller"]["etcdPrefix"], "/sdn/docker/controller")
        self.assertEqual(controller["controller"]["etcd"], "http://127.0.0.1:2379")
        self.assertEqual(controller["etcd"]["dataDir"], "/etcd-data")
        for node in ("spine", "leaf1", "leaf2"):
            value = json.loads((self.config / node / "agent.conf").read_text())
            self.assertEqual(value["agent"]["endpoint"], node+":9090")
            self.assertEqual(value["agent"]["redis"], "127.0.0.1:6379")
            self.assertEqual(value["redis"]["appendFsync"], "always")
            self.assertEqual(value["redis"]["dataDir"], "/var/lib/redis")
            self.assertNotIn("token", value["agent"])

    def test_existing_custom_file_is_preserved_byte_for_byte(self):
        manage.ensure_configs()
        target = self.config / "leaf1/agent.conf"
        custom = '{"agent":{"endpoint":"custom-node:9090"},"redis":{"port":16379}}\n'
        target.write_text(custom)
        self.assertEqual(manage.ensure_configs(), [])
        self.assertEqual(target.read_text(), custom)

    def test_missing_file_is_recreated_without_replacing_other_configs(self):
        manage.ensure_configs()
        keep = self.config / "leaf1/agent.conf"
        before = keep.read_bytes()
        (self.config / "leaf2/agent.conf").unlink()
        self.assertEqual(manage.ensure_configs(), ["config/leaf2/agent.conf"])
        self.assertEqual(keep.read_bytes(), before)

    def test_invalid_json_or_directory_is_not_silently_replaced(self):
        target = self.config / "leaf1/agent.conf"
        target.mkdir(parents=True)
        with self.assertRaisesRegex(RuntimeError, "Expected configuration file"):
            manage.ensure_configs()
        target.rmdir()
        target.write_text("[invalid JSON")
        with self.assertRaises(json.JSONDecodeError):
            manage.ensure_configs()
        self.assertEqual(target.read_text(), "[invalid JSON")

    def test_non_object_config_rejected(self):
        manage.ensure_configs()
        (self.config / "leaf1/agent.conf").write_text("[]")
        with self.assertRaisesRegex(RuntimeError, "JSON object"):
            manage.ensure_configs()


class LegacyMigrationTests(unittest.TestCase):
    def test_only_stopped_owned_container_ids_are_removed_without_volumes(self):
        calls = []
        def run(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("docker", "inspect"):
                return result(code=0 if args[2].endswith("etcd") else 1)
            return result()
        with patch.object(manage, "run", side_effect=run), patch.object(manage, "owned", return_value={"Id": "owned-etcd-id", "State": {"Running": False}}):
            manage.retire_legacy_databases()
        self.assertEqual([c for c in calls if c[:2] == ("docker", "rm")], [("docker", "rm", "owned-etcd-id")])

    def test_running_legacy_database_blocks_all_removal(self):
        calls = []
        def run(*args, **kwargs):
            calls.append(args)
            return result()
        def owned(node):
            return {"Id": node, "State": {"Running": node == "redis-spine"}}
        with patch.object(manage, "run", side_effect=run), patch.object(manage, "owned", side_effect=owned):
            with self.assertRaisesRegex(RuntimeError, "Stop the old database"):
                manage.retire_legacy_databases()
        self.assertFalse(any(c[:2] == ("docker", "rm") for c in calls))

    def test_ownership_failure_blocks_removal(self):
        with patch.object(manage, "run", return_value=result()) as run, patch.object(manage, "owned", side_effect=RuntimeError("Refusing unrelated")):
            with self.assertRaisesRegex(RuntimeError, "Refusing unrelated"):
                manage.retire_legacy_databases()
        self.assertFalse(any(c.args[:2] == ("docker", "rm") for c in run.call_args_list))

    def test_unknown_project_container_blocks_reset_validation(self):
        with patch.object(manage, "run", return_value=result("sdn-docker-unexpected\n")):
            with self.assertRaisesRegex(RuntimeError, "Refusing unknown containers"):
                manage.validate_owned_resources()


class LifecycleCommandTests(unittest.TestCase):
    def test_build_selects_one_service_per_distinct_image(self):
        with patch.object(manage.sys, "argv", ["manage.py", "build"]), patch.object(manage, "initialize"), patch.object(manage.subprocess, "run") as run:
            manage.main()
        run.assert_called_once_with(manage.COMPOSE+["build", "controller", "spine", "web"], check=True)

    def test_reset_budget_covers_dependency_ordered_shutdown(self):
        with patch.object(manage, "validate_owned_resources") as validate, patch.object(manage, "compose") as compose, patch.object(manage, "up"), patch.object(manage, "verify_clean"):
            manage.reset()
        validate.assert_called_once()
        compose.assert_called_once_with("--profile", "tools", "--profile", "lab", "down", "--volumes", "--remove-orphans", timeout=420)

    def test_up_selects_only_five_core_services(self):
        with patch.object(manage, "initialize"), patch.object(manage, "validate_owned_resources"), patch.object(manage, "retire_legacy_databases"), patch.object(manage, "retire_legacy_gateway"), patch.object(manage, "stop_lab_hosts") as stop, patch.object(manage, "compose") as compose, patch.object(manage, "wire"), patch.object(manage, "wait_ready", return_value=[]), patch.object(manage, "render_example"), patch.object(manage.Path, "exists", return_value=True):
            manage.up()
        stop.assert_called_once()
        self.assertEqual(compose.call_args.args[-5:], manage.SERVICES)
        self.assertNotIn("pc1", compose.call_args.args)

    def test_lab_up_is_explicit_opt_in(self):
        with patch.object(manage, "initialize"), patch.object(manage, "validate_owned_resources"), patch.object(manage, "retire_legacy_databases"), patch.object(manage, "retire_legacy_gateway"), patch.object(manage, "stop_lab_hosts") as stop, patch.object(manage, "compose") as compose, patch.object(manage, "wire"), patch.object(manage, "wait_ready", return_value=[]), patch.object(manage, "render_example"), patch.object(manage.Path, "exists", return_value=True):
            manage.up(lab=True)
        stop.assert_not_called()
        self.assertEqual(compose.call_args.args[:2], ("--profile", "lab"))
        self.assertEqual(compose.call_args.args[-2:], manage.LAB_SERVICES)


if __name__ == "__main__":
    unittest.main()
