import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('install_docs', Path(__file__).resolve().parents[1] / 'ops/install-docs.py')
install_docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(install_docs)


class DocsArtifactTest(unittest.TestCase):
    def test_invalid_artifacts_are_rejected_before_network_or_system_changes(self):
        with tempfile.TemporaryDirectory() as folder:
            archive = Path(folder) / 'docs.tgz'
            archive.write_bytes(b'corrupt')
            with patch.object(install_docs.os, 'geteuid', return_value=0), \
                    patch.object(install_docs, 'urlopen') as network, \
                    patch.object(install_docs.subprocess, 'run') as commands:
                with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                    install_docs.install(archive, '0' * 64)
                for name in ['../outside', '/etc/nginx/nginx.conf', 'unexpected.txt']:
                    buffer = io.BytesIO()
                    with tarfile.open(fileobj=buffer, mode='w:gz') as source:
                        source.addfile(tarfile.TarInfo(name))
                    archive.write_bytes(buffer.getvalue())
                    with self.assertRaisesRegex(ValueError, 'Unexpected artifact contents'):
                        install_docs.install(archive, install_docs.sha256(buffer.getvalue()))
                network.assert_not_called()
                commands.assert_not_called()
