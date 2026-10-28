// Run: node prototypes/frontend/submit.test.cjs
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(__dirname + '/submit.js', 'utf8');
// Exercise the same review gate used by the browser, without a DOM dependency.
const pure = source.slice(source.indexOf('const sides'), source.indexOf('let draft=')) +
  source.slice(source.indexOf('function validWork('), source.indexOf('function fitTitle('));
const context = vm.createContext({});
vm.runInContext(pure + '\nthis.exampleDraft=exampleDraft;this.emptyDraft=emptyDraft;this.checks=checks;this.healthComplete=healthComplete;', context);
const ready = d => context.checks(d).every(([, ok]) => ok !== false);
const draft = context.exampleDraft();
assert.equal(ready(draft), true);
draft.works.agent = [];
assert.equal(ready(draft), false, 'A single side cannot be sent for review');
const noPermission = context.exampleDraft();
noPermission.works.human[0].cloud = '';
assert.equal(ready(noPermission), false, 'Cloud-use choice must be explicit');
const missingModel = context.exampleDraft();
missingModel.works.agent[0].model = '';
assert.equal(ready(missingModel), false, 'Agent works require a model');
const linkOnly = context.exampleDraft();
linkOnly.works.human[0].body = '';
linkOnly.works.human[0].url = 'https://example.com';
assert.equal(ready(linkOnly), false, 'A link does not replace actual work');
const missingFile = context.exampleDraft();
missingFile.attachments.push({name:'brief.txt',available:false,cloud:'no'});
assert.equal(ready(missingFile), false, 'Restored file names are not uploaded files');
assert.equal(ready(context.emptyDraft()),false,'Empty chapters cannot pass through vacuous every()');
assert.equal(context.checks(context.emptyDraft())[3][1],false);
assert.equal(context.checks(context.emptyDraft())[4][1],null,'No optional files is neutral, not a completed upload');
assert.equal(context.healthComplete({status:'ok',revision:'abc'}),false,'Partial health must not imply availability');
assert.equal(context.healthComplete({status:'ok',revision:'abc',deploymentRevision:'abc',deployedServices:['identity','content','asset','moderation','gateway']}),true);
assert.equal(context.healthComplete({status:'ok',revision:'abc',deploymentRevision:'old',deployedServices:['identity','content','asset','moderation','gateway']}),false);
assert.equal(context.healthComplete({status:'error',revision:'abc',deploymentRevision:'abc',deployedServices:['identity','content','asset','moderation','gateway']}),false);
const incomplete = context.exampleDraft(); incomplete.works.human[0].source=' '; assert.equal(context.checks(incomplete)[1][1],false);
console.log('Submit validation and health gates: 14 checks passed.');
