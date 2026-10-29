#!/usr/bin/env python3
"""Response-schema regressions; run with the acceptance Schemathesis environment.

Uses its existing PyYAML and jsonschema-rs dependencies. OPENAPI_SCHEMA_FILE can
select an earlier contract to demonstrate that the original defects are caught.
These synthetic samples complement, rather than replace, real HTTP acceptance.
"""

import copy
import json
import os
from pathlib import Path
import unittest

from jsonschema_rs import Draft202012Validator
import yaml


ROOT = Path(__file__).resolve().parents[2]
SAMPLES = ROOT / 'tests/fixtures/api-response-contract.json'
DOWNLOADS = [
    '/api/me/assets/{assetId}/content',
    '/api/tasks/{taskId}/entries/{entryId}/assets/{assetId}/content',
    '/api/tasks/{taskId}/assets/{assetId}/content',
    '/api/admin/reviews/{reviewId}/assets/{assetId}/content',
]
MEDIA_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'application/pdf', 'text/plain']


class ResponseContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(os.environ.get('OPENAPI_SCHEMA_FILE', ROOT / 'openapi.yaml'))
        cls.document = yaml.safe_load(path.read_text())
        cls.samples = json.loads(SAMPLES.read_text())

    def validator(self, name):
        return Draft202012Validator({
            'components': self.document['components'],
            '$ref': '#/components/schemas/' + name,
        }, validate_formats=True)

    def test_public_attachment_projection(self):
        value = self.samples['publicTask']
        validator = self.validator('PublicTask')
        validator.validate(value)
        for field in ['state', 'createdAt', 'completedAt', 'ownerId', 'runId']:
            with self.subTest(private_field=field):
                invalid = copy.deepcopy(value)
                invalid['taskAttachments'][0][field] = 'must-not-be-public'
                self.assertFalse(validator.is_valid(invalid))
        invalid = copy.deepcopy(value)
        invalid['taskAttachments'][0]['sha256'] = ''
        self.assertFalse(validator.is_valid(invalid))

    def test_staging_hash_is_not_a_verified_hash(self):
        validator = self.validator('Asset')
        value = self.samples['stagingAsset']
        validator.validate(value)
        invalid = dict(value, sha256='a' * 64)
        self.assertFalse(validator.is_valid(invalid))

    def test_ready_hash_stays_strict(self):
        validator = self.validator('Asset')
        value = self.samples['readyAsset']
        validator.validate(value)
        for digest in ['', 'abc', 'A' * 64, 'g' * 64, None]:
            with self.subTest(digest=digest):
                self.assertFalse(validator.is_valid(dict(value, sha256=digest)))
        missing = dict(value)
        del missing['sha256']
        self.assertFalse(validator.is_valid(missing))
        for state in ['failed', 'deleted']:
            for digest in ['', value['sha256']]:
                with self.subTest(state=state, has_hash=bool(digest)):
                    validator.validate(dict(value, state=state, sha256=digest))

    def test_public_comment_author(self):
        validator = self.validator('Comment')
        value = self.samples['comment']
        validator.validate(value)
        named = copy.deepcopy(value)
        named['author']['displayName'] = 'Synthetic contributor'
        validator.validate(named)
        for invalid_name in [123, {}, []]:
            with self.subTest(name=invalid_name):
                invalid = copy.deepcopy(value)
                invalid['author']['displayName'] = invalid_name
                self.assertFalse(validator.is_valid(invalid))

    def test_session_author_remains_non_null(self):
        validator = self.validator('Session')
        value = self.samples['session']
        validator.validate(value)
        invalid = copy.deepcopy(value)
        invalid['account']['displayName'] = None
        self.assertFalse(validator.is_valid(invalid))

    def test_all_download_contexts_support_actual_media_types(self):
        for path in DOWNLOADS:
            with self.subTest(path=path):
                content = self.document['paths'][path]['get']['responses']['200']['content']
                for media in MEDIA_TYPES:
                    self.assertIn(media, content)
                    self.assertEqual(content[media]['schema'], {'type': 'string', 'format': 'binary'})
                self.assertNotIn('text/html', content)
                self.assertNotIn('*/*', content)


if __name__ == '__main__':
    unittest.main()
