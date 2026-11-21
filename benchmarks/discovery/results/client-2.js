const {owner,repo,tag}=input;
const enc=encodeURIComponent, root=`/repos/${enc(owner)}/${enc(repo)}/git`;
const signed=o=>Boolean(o.verification.signature);
let {object}=await get(`${root}/ref/tags/${tag.split('/').map(enc).join('/')}`);
const tag_chain=[];
while(object.type==='tag'){
 const t=await get(`${root}/tags/${enc(object.sha)}`);
 tag_chain.push({sha:t.sha,signed:signed(t)});
 object=t.object;
}
if(object.type!=='commit') throw new Error('Tag does not resolve to a commit');
const c=await get(`${root}/commits/${enc(object.sha)}`);
return {commit_sha:c.sha,commit_subject:c.message.split(/\r?\n/,1)[0],commit_signed:signed(c),tag_chain};