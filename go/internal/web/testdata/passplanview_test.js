const assert = require('assert'); global.window = {};
class E {constructor(tag) {this.tag=tag;this.children=[];this.text='';} set textContent(t){this.text=String(t);this.children=[];}get textContent(){return this.text+this.children.map(c=>c.textContent).join('');}appendChild(c){this.children.push(c);}}
const host=new E('div');global.document={getElementById:id=>id==='observation-plan'?host:null,createElement:tag=>new E(tag)};
require(process.argv[2]);const P=window.SDRPassplan;
assert.equal(P.describe({enabled:false}).rows.length,0);
assert.match(P.describe({enabled:true}).note,/Waiting for cached/);
P.render({enabled:true,info:{observer:'receiver',error:'missing elements'},observations:[{satellite:'<script>alert(1)</script>',status:'failed',detail:'<img onerror=x>',peak_elevation:35,resource:'offline-fixture',task_start:'start',task_end:'end'}]});
assert.match(host.textContent,/receiver location/);assert.match(host.textContent,/missing elements/);assert.match(host.textContent,/<script>alert/);assert.match(host.textContent,/failed/);
assert.equal(host.children.filter(c=>c.tag==='script').length,0);
P.render(null,'HTTP 500');assert.match(host.textContent,/No current plan state verified/);assert.doesNotMatch(host.textContent,/alert/);
P.render({enabled:true,observations:[]});assert.match(host.textContent,/No planned observations/);
console.log('planner view disabled, empty, error, failed state and text-only render passed');
