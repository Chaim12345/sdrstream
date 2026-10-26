global.window={};require(process.argv[2]);const C=window.SDRCoverage;const a=(x,y)=>{if(x!==y)throw new Error(x+' != '+y)};
a(C.coverage({minHz:0,maxHz:30e6},'atc'),'no');a(C.coverage({minHz:0,maxHz:30e6},'ham'),'covers');a(C.coverage({minHz:0,maxHz:30e6},'marine'),'covers');
a(C.coverage({minHz:24e6,maxHz:1766e6},'atc'),'covers');a(C.coverage({},'atc'),'unknown');a(C.coverage({minHz:5,maxHz:1},'atc'),'unknown');a(C.coverage({minHz:0,maxHz:30e6},'weather'),'no');
console.log('coverage ok');
