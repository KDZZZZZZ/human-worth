#!/usr/bin/env python3
"""Round 3 schema regressions; complements real Gateway/Inspector acceptance."""
import os
from pathlib import Path
import unittest
import yaml
from jsonschema_rs import Draft202012Validator

ROOT = Path(__file__).resolve().parents[2]

class Round3ContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.document = yaml.safe_load(Path(os.environ.get('OPENAPI_SCHEMA_FILE', ROOT / 'openapi.yaml')).read_text())

    def validator(self, schema):
        return Draft202012Validator({'components': self.document['components'], **schema})

    def test_restart_envelope_matches_existing_http_clients(self):
        schema = self.document['paths']['/api/admin/challenge-runs/{runId}/restart']['post']['requestBody']['content']['application/json']['schema']
        validator = self.validator(schema)
        configuration = {'initialTaskPackage': {'taskRevision': 1, 'description': 'synthetic', 'workRequirements': 'synthetic'}, 'packer': {'model': 'fixture-model'}, 'ranker': {'model': 'fixture-model'}, 'executor': {'model': 'fixture-model', 'harness': 'completion-shell'}, 'rankerFitThreshold': .8, 'rankerMinComparablePairs': 1, 'rankerRoundLimit': 1, 'roundLimit': 1, 'budget': {'amount': 30, 'unit': 'model_calls'}}
        validator.validate({'configuration': configuration})
        self.assertFalse(validator.is_valid(configuration))
        self.assertFalse(validator.is_valid({}))
        self.assertFalse(validator.is_valid({'configuration': configuration, 'taskId': 'forged'}))

    def test_absent_attachments_mean_empty_selection(self):
        validator = self.validator({'$ref': '#/components/schemas/InitialTaskPackage'})
        base = {'taskRevision': 1, 'description': 'synthetic', 'workRequirements': 'synthetic'}
        validator.validate(base)
        validator.validate(dict(base, taskAttachmentIds=[]))
        for value in [None, 'asset', {}, ['duplicate', 'duplicate']]:
            with self.subTest(value=value):
                self.assertFalse(validator.is_valid(dict(base, taskAttachmentIds=value)))

    def test_mcp_400_declares_protocol_and_transport_errors(self):
        response = self.document['paths']['/api/mcp']['post']['responses']['400']
        content = response['content']
        rpc = self.validator(content['application/json']['schema'])
        problem = self.validator(content['application/problem+json']['schema'])
        for code in [-32700, -32600, -32602, -32022]:
            rpc.validate({'jsonrpc': '2.0', 'id': None, 'error': {'code': code, 'message': 'synthetic error'}})
        rpc.validate({'jsonrpc': '2.0', 'id': 'request', 'error': {'code': -32022, 'message': 'unsupported', 'data': {'supportedVersions': ['2025-11-25']}}})
        self.assertFalse(rpc.is_valid({'jsonrpc': '2.0', 'id': None, 'error': {'code': 'wrong type', 'message': 'synthetic'}}))
        self.assertFalse(rpc.is_valid({'jsonrpc': '2.0', 'id': None, 'result': {}}))
        problem.validate({'type': 'about:blank', 'title': 'Bad Request', 'status': 400, 'detail': 'Bad Request', 'code': 'invalid_request'})

if __name__ == '__main__':
    unittest.main()
