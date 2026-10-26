const fs=require('fs'),vm=require('vm'),assert=require('assert');
const app=fs.readFileSync(process.argv[2],'utf8');
const status=app.slice(app.indexOf('async function refreshStatus()'),app.indexOf('/* ---------- passes view ---------- */'));
let renders=[],calls=[];const msg={};
const ctx={window:{SDRPassplan:{render:(...args)=>renders.push(args)}},el:()=>msg,fetchJSON:async url=>{calls.push(url);if(url==='/api/status.json')throw new Error('status broken');return {enabled:true,observations:[]};},console};
vm.createContext(ctx);vm.runInContext(status,ctx);
(async()=>{await ctx.refreshStatus();await new Promise(r=>setImmediate(r));assert(calls.includes('/api/passplan.json'));assert.equal(renders.length,1);assert.equal(renders[0][0].enabled,true);assert.match(msg.textContent,/status unavailable/);
 renders=[];ctx.fetchJSON=async()=>{throw new Error('both failed')};await ctx.refreshStatus();await new Promise(r=>setImmediate(r));assert.equal(renders.length,1);assert.equal(renders[0][0],null);assert.match(renders[0][1],/both failed/);console.log('planner refresh survives status failure and renders own failure');})().catch(e=>{console.error(e);process.exitCode=1});
