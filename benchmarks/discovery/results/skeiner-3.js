const {owner,repo,tag}=api.input;
const read=(op,p)=>api.call(op,{path:{owner,repo,...p}});
const signed=o=>Boolean(o.verification.signature);
let {object}=await read('getTagReference',{tag});
const tag_chain=[];
while(object.type==='tag'){
  const t=await read('getAnnotatedTag',{tag_sha:object.sha});
  tag_chain.push({sha:t.sha,signed:signed(t)});
  object=t.object;
}
if(object.type!=='commit') throw new Error('Tag does not resolve to a commit');
const c=await read('getGitCommit',{commit_sha:object.sha});
return {commit_sha:c.sha,commit_subject:c.message.split(/\r?\n/,1)[0],commit_signed:signed(c),tag_chain};