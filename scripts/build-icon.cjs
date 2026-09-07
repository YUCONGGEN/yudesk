// Rasterize the code-native Yu mark and package a Windows multi-size ICO.
// Run only at asset-authoring time; the generated .ico and .syso are versioned.
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
(async()=>{
  const browser=await chromium.launch({executablePath:process.env.YUDESK_TEST_BROWSER,headless:true});
  try{
    const svg=fs.readFileSync(path.resolve('internal/viewerapp/ui/icon.svg'),'utf8');
    const sizes=[16,24,32,48,64,128,256],images=[];
    const page=await browser.newPage();
    for(const size of sizes){await page.setViewportSize({width:size,height:size});await page.setContent('<style>html,body{margin:0;width:100%;height:100%;background:transparent}svg{display:block;width:100%;height:100%}</style>'+svg);images.push(await page.screenshot({omitBackground:true}));}
    const header=Buffer.alloc(6+16*sizes.length);header.writeUInt16LE(1,2);header.writeUInt16LE(sizes.length,4);
    let offset=header.length;
    for(let i=0;i<sizes.length;i++){const at=6+i*16;header[at]=header[at+1]=sizes[i]===256?0:sizes[i];header.writeUInt16LE(1,at+4);header.writeUInt16LE(32,at+6);header.writeUInt32LE(images[i].length,at+8);header.writeUInt32LE(offset,at+12);offset+=images[i].length;}
    fs.writeFileSync(path.resolve('internal/viewerapp/ui/yudesk.ico'),Buffer.concat([header,...images]));
    fs.writeFileSync(path.resolve('.smoke/yu-icon.png'),images[6]);
    console.log('Generated blue Yu icon: '+sizes.join(', ')+'px');
  }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
