const {owner,repo,tag}=api.input;
const read=(op,p)=>api.call(op,{path:{owner,repo,...p}});
const signed=o=>typeof o.verification.signature==='string' && o.verification.signature.length>0;
let obj=(await read('getTagReference',{tag})).object;
const tag_chain=[];
while(obj.type==='tag'){
 const t=await read('getAnnotatedTag',{tag_sha:obj.sha});
 tag_chain.push({sha:t.sha,signed:signed(t)});
 obj=t.object;
}
if(obj.type!=='commit') throw new Error('Tag does not resolve to a commit');
const c=await read('getGitCommit',{commit_sha:obj.sha});
return {commit_sha:c.sha,commit_subject:c.message.split(/\r?\n/,1)[0],commit_signed:signed(c),tag_chain};