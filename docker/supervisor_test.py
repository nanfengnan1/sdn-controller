"""Controller supervisor regressions; no database, container or network mutation."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('controller_supervisor', Path(__file__).with_name('supervisor.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class UtilityTests(unittest.TestCase):
    def test_all_go_boolean_spellings_and_aliases(self):
        for flag in ('check-config', 'runtime-plan', 'healthcheck'):
            for prefix in ('-', '--'):
                for value in ('0', 'f', 'F', 'FALSE', 'false', 'False'):
                    with self.subTest(flag=flag, prefix=prefix, value=value):
                        self.assertFalse(module.utility_only([prefix + flag + '=' + value]))
                for value in ('1', 't', 'T', 'TRUE', 'true', 'True'):
                    with self.subTest(flag=flag, prefix=prefix, value=value):
                        self.assertTrue(module.utility_only([prefix + flag + '=' + value]))
                self.assertTrue(module.utility_only([prefix + flag]))

    def test_last_occurrence_wins_for_each_mode(self):
        for flag in ('check-config', 'runtime-plan', 'healthcheck'):
            self.assertFalse(module.utility_only(['-' + flag, '--' + flag + '=0']))
            self.assertTrue(module.utility_only(['-' + flag + '=False', '--' + flag + '=T']))
        self.assertTrue(module.utility_only(['-check-config', '-runtime-plan=false']))
        self.assertFalse(module.utility_only(['-check-config', '-runtime-plan', '-check-config=0', '-runtime-plan=F']))

    def test_string_values_and_positional_stop_are_not_diagnostic_flags(self):
        self.assertFalse(module.utility_only(['-state-file', '-check-config', '-insecure']))
        self.assertFalse(module.utility_only(['-config=-runtime-plan', '-insecure']))
        self.assertFalse(module.utility_only(['--', '-check-config']))
        self.assertFalse(module.utility_only(['positional', '-check-config']))

    def test_help_and_invalid_flags_delegate_to_go_without_starting_services(self):
        for args in (['-h'], ['--help'], ['--h=false'], ['-unknown'], ['-check-config=invalid', '-check-config=false']):
            with self.subTest(args=args):
                self.assertTrue(module.utility_only(args))

    def test_disabled_diagnostic_uses_supervised_validation_path(self):
        for args in (['-check-config=0'], ['-check-config', '-check-config=false'], ['--healthcheck=F'], ['-runtime-plan=True', '-runtime-plan=False']):
            with self.subTest(args=args), patch.object(module, 'load_plan', side_effect=RuntimeError('validation marker')) as plan, patch.object(module.subprocess, 'run') as raw, patch.object(module.subprocess, 'Popen') as spawn, patch.object(module.signal, 'signal'):
                with self.assertRaisesRegex(RuntimeError, 'validation marker'):
                    module.main(args)
                plan.assert_called_once_with(module.app_arguments(args))
                raw.assert_not_called()
                spawn.assert_not_called()

    def test_enabled_diagnostic_never_starts_bundled_services(self):
        with patch.object(module.subprocess, 'run', return_value=Mock(returncode=0)) as raw, patch.object(module, 'load_plan') as plan, patch.object(module.subprocess, 'Popen') as spawn:
            self.assertEqual(module.main(['--check-config=True']), 0)
            raw.assert_called_once_with([module.APP, *module.app_arguments(['--check-config=True'])], check=False)
            plan.assert_not_called()
            spawn.assert_not_called()


if __name__ == '__main__':
    unittest.main()
