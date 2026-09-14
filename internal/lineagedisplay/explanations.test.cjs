const { test } = require('node:test');
const assert = require('node:assert/strict');
const { build } = require('./explanations.js');

const ref = (kind, name) => ({kind, name, uid: name + '-uid'});
const node = (kind, name) => ({id: name, kind, resource: ref(kind, name), sourceEvents: [name + '-event']});
const edge = (from, relation, to, eventId) => ({from, relation, to, eventId});

function recovery() {
  const story = {kind:'recovery', eventId:'retry', status:'linked', nodes:[
    {...node('StepAttempt','prior'), outcome:'Succeeded'}, node('StepAttempt','failure'), node('StepAttempt','replacement'),
    {id:'decision', kind:'RecoveryDecision', sourceEvents:['selection']}
  ], links:[edge('replacement','authorized_by','decision','retry'), edge('replacement','triggered_by','failure','retry'), edge('replacement','retry_of','prior','retry')]};
  const events = new Map([
    ['selection',{evidence:{restartStep:'test-author', decision:{revision:'recovery/v1'}}}],
    ['retry',{evidence:{attempt:ref('StepAttempt','replacement'), decisionEvent:'selection', inputs:[]}}]
  ]);
  return {story,events};
}

function approval() {
  const pin = {name:'candidate-revision', artifactRef:ref('Artifact','candidate'), digest:'sha256:exact'};
  const human = {...node('ApprovalDecision','human'), approval:{choice:'Approved', approver:{subjectId:'alice'}, stepAttempt:ref('StepAttempt','review'), submissionEvent:'submission', inputEvent:'review-inputs', reviewedInputs:[pin]}};
  const story = {kind:'approval', eventId:'approval', status:'linked', nodes:[human, node('UtilityOperation','merge'), node('StepAttempt','merge-attempt'), {id:'auth',kind:'UtilityAuthorization',sourceEvents:['authorization']}], links:[
    edge('merge','approved_by','human','admission'), edge('merge','authorized_by','auth','authorization'), edge('auth','authorized_by','merge-attempt','authorization')
  ]};
  const events = new Map([
    ['admission',{evidence:{approvalBinding:{requirement:{step:'product-approval',subject:'candidate-revision'},subject:pin}}}],
    ['authorization',{evidence:{inputEvent:'authorized-inputs'}}],
    ['authorized-inputs',{evidence:{inputs:[{contract:'candidate-revision/v1',artifact:pin.artifactRef,digest:pin.digest}]}}]
  ]);
  return {story,events};
}

test('recovery distinguishes trigger, repeated successful attempt, and creation', () => {
  const {story,events} = recovery();
  const result = build(story,events,'selection');
  assert.equal(result.complete,true);
  assert.deepEqual(result.highlights.map(item=>[item.ref.name,item.label]), [['failure','Triggered this recovery'],['prior','Attempt being repeated'],['replacement','Created by this recovery']]);
  assert.match(result.roles.find(role=>role.label==='Repeats').value,/Succeeded/);
  assert(result.roles.every(role=>role.eventId));
  assert(!result.decisionEvents.includes('failure'));
});

test('missing, mismatched and unavailable sources cannot produce affirmative explanations', () => {
  for (const status of ['missing evidence','evidence mismatch']) {
    const {story,events}=recovery();story.status=status;
    const result=build(story,events,'selection');
    assert.equal(result.complete,false);assert.equal(result.highlights.length,0);
  }
  const {story,events}=recovery();events.delete('selection');
  assert.equal(build(story,events,'retry').complete,false);
});

test('approval exposes the exact reviewed and authorized pin without inventing completion', () => {
  const {story,events}=approval();
  const result=build(story,events,'authorization');
  assert.equal(result.complete,true);
  const reviewed=result.roles.find(role=>role.label==='Reviewed subject').pins[0];
  const authorized=result.roles.find(role=>role.label==='Authorization input').pins[0];
  assert.deepEqual(reviewed,authorized);
  assert.equal(result.roles.some(role=>role.label==='Reported result'),false);
  assert(result.decisionEvents.includes('admission'));
  events.set('wrong-completion',{type:'UtilityOperationCompleted',outcome:'succeeded',evidence:{decisionEvent:'unrelated-authorization'}});
  assert.equal(build(story,events,'approval').roles.some(role=>role.label==='Reported result'),false);
  events.set('completion',{type:'UtilityOperationCompleted',eventId:'completion',outcome:'failed',evidence:{decisionEvent:'authorization'}});
  assert.equal(build(story,events,'approval').roles.find(role=>role.label==='Reported result').value,'failed');
});

test('different candidate content or identity blocks the exact-match claim', () => {
  for (const field of ['uid','digest']) {
    const {story,events}=approval();
    const input=events.get('authorized-inputs').evidence.inputs[0];
    if(field==='uid')input.artifact={...input.artifact,uid:'other'};else input.digest='other';
    assert.equal(build(story,events,'authorization').complete,false);
  }
});

test('denial never implies permission and does not highlight an authorized operation', () => {
  const {story,events}=approval();story.nodes[0].approval.choice='Denied';story.links=[];
  const result=build(story,events,'approval');
  assert.equal(result.complete,true);assert.equal(result.highlights.length,1);
  assert.match(result.summary,/not an execution authorization/);
});

test('reordering linked evidence does not change the explanation', () => {
  const {story,events}=recovery();const first=build(story,events,'selection');
  story.nodes.reverse();story.links.reverse();
  assert.deepEqual(build(story,new Map([...events].reverse()),'selection'),first);
});
