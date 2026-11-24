#!/usr/bin/env python3
"""End-to-end gateway + real ntfy integration. Creates only explicitly named test resources.
Environment: QQ_TEST_URL, QQ_TEST_ADMIN_PASSWORD_FILE. Never prints credentials.
Run against an isolated test deployment, not a production account.
"""
import os,json,time,urllib.request,urllib.error,pathlib,sys
base=os.environ.get('QQ_TEST_URL','http://127.0.0.1:18080').rstrip('/')
def request(path,payload=None,token=None,method=None,headers=None,raw=False):
 h=dict(headers or {});data=None
 if payload is not None:
  if isinstance(payload,dict):data=json.dumps(payload,ensure_ascii=False).encode();h['Content-Type']='application/json'
  else:data=payload.encode();h.setdefault('Content-Type','text/plain')
 if token:h['Authorization']='Bearer '+token
 req=urllib.request.Request(base+path,data=data,headers=h,method=method)
 try:
  with urllib.request.urlopen(req,timeout=30) as r:
   b=r.read();return r.status,b.decode() if raw else json.loads(b)
 except urllib.error.HTTPError as e:
  b=e.read();return e.code,b.decode() if raw else json.loads(b)
def must(path,payload=None,token=None,method=None,headers=None,raw=False):
 code,result=request(path,payload,token,method,headers,raw)
 assert code<300,(path,code,result)
 return result
password=pathlib.Path(os.environ['QQ_TEST_ADMIN_PASSWORD_FILE']).read_text().strip()
admin=must('/api/v1/login',{'name':'admin','password':password})['token']
name='integration-'+str(time.time_ns());password=os.urandom(24).hex()
u=must('/api/v1/manage/users',{'name':name,'password':password,'role':'user'},admin)
session=must('/api/v1/login',{'name':name,'password':password})['token']
paired=[]
for suffix in ['one','two']:
 code=must('/api/v1/manage/pairings',{},session)['code']
 d=must('/api/v1/pair/claim',{'code':code,'name':'integration-phone-'+suffix})
 assert request('/api/v1/pair/claim',{'code':code,'name':'duplicate'})[0]==401
 paired.append(d)
topic=must('/api/v1/manage/topics',{'name':'integration-server'},session)
for d in paired:must('/api/v1/manage/bindings',{'topic_id':topic['id'],'device_id':d['device_id']},session)
keys={}
for kind in ['native','serverchan','sc3','pushplus','bark','wxpusher','ntfy']:
 keys[kind]=must('/api/v1/manage/keys',{'kind':kind,'label':'integration-'+kind,'targets':{'device_ids':[paired[0]['device_id']],'topic_ids':[topic['id']]}},session)
# One message per adapter, including full cached body beyond FCM payload size.
cases=[('/api/v1/push',{'token':keys['native']['token'],'title':'中文','body':'中'*3000,'topic':topic['name'],'business_type':'server','sound':'alarm'}),
('/sctapi/'+keys['serverchan']['token']+'.send',{'title':'告警','desp':'**中文**'}),
('/sc3/'+keys['sc3']['sc3_uid']+'/send/'+keys['sc3']['token']+'.send',{'text':'SC3','desp':'正文'}),
('/send',{'token':keys['pushplus']['token'],'title':'PushPlus','content':'<b>完成</b><script>bad()</script>','template':'html'}),
('/push',{'device_key':keys['bark']['token'],'title':'Bark','body':'正文','group':'server','sound':'alarm','icon':'server'}),
('/api/send/message',{'appToken':keys['wxpusher']['token'],'summary':'WxPusher','content':'正文','uids':[u['id']],'topicIds':[topic['id']]})]
for path,p in cases:must(path,p)
# Native ntfy sends keep their original paths/headers/response and group fanout.
native=keys['ntfy'];group=next(t['topic'] for t in native['native_topics'] if 'topic_id' in t)
original=must('/'+group,'native raw body',native['token'],headers={'Host':'ntfy.localhost','Title':'Native title','Priority':'high','Click':'https://example.com','Tags':'server,warning','Actions':'view, Open, https://example.com'})
assert original['event']=='message' and original['actions'][0]['action']=='view'
assert request('/'+paired[1]['topic'],'unauthorized',native['token'],headers={'Host':'ntfy.localhost'})[0]==403
# A native attachment remains accessible through the bound device's upstream read grant.
attachment=must('/'+group,'integration attachment',native['token'],method='PUT',headers={'Host':'ntfy.localhost','Content-Type':'application/octet-stream','Filename':'integration.txt'})
assert 'attachment' in attachment
# Wait for the real upstream to accept each delivery.
for _ in range(40):
 messages=must('/api/v1/manage/messages',token=session)['data']
 if len(messages)==8 and all(m['status']=='published' for m in messages):break
 time.sleep(.5)
else:raise AssertionError([(m['protocol'],m['status']) for m in messages])
# Authenticated fetch of private cache with device-specific read credentials.
for index,d in enumerate(paired):
 raw=must('/'+d['topic']+'/json?poll=1',token=d['ntfy_token'],headers={'Host':'ntfy.localhost'},raw=True)
 cached=[json.loads(line) for line in raw.splitlines() if line]
 assert len(cached)==8,(index,len(cached))
 assert any(len(m['message'])==3000 for m in cached)
 assert any(m.get('qiuqiu',{}).get('business_type')=='server' for m in cached)
 assert any(m.get('actions') for m in cached),'native actions lost'
 attachments=[m['attachment'] for m in cached if m.get('attachment')]
 assert attachments,'native attachment lost'
 path=urllib.parse.urlparse(attachments[0]['url']).path
 attachment_body=must(path,token=d['ntfy_token'],headers={'Host':'ntfy.localhost'},raw=True)
 assert attachment_body=='integration attachment'
 assert request('/'+d['topic']+'/json?poll=1',headers={'Host':'ntfy.localhost'},raw=True)[0]==403
assert request('/'+paired[1]['topic']+'/json?poll=1',token=paired[0]['ntfy_token'],headers={'Host':'ntfy.localhost'},raw=True)[0]==403
# Live tracking is backed by the actual delivery rows and authenticated receipt.
tracked=messages[0]['id']
receipt={'receipts':[{'message_id':tracked,'stage':'received'},{'message_id':tracked,'stage':'notification_submitted'}]}
ack=must('/api/v1/device/receipts',receipt,paired[0]['device_token'])
assert all(x['status']=='recorded' for x in ack['results'])
trace=must('/api/v1/manage/messages/'+tracked,token=session)
assert len(trace['deliveries'])==2 and len(trace['receipts'])==2
assert any(x['stage']=='received' for x in trace['events'])
assert any(x['stage']=='notification_submitted' for x in trace['events'])
must('/api/v1/manage/devices/profile',{'id':paired[0]['device_id'],'name':'友好的手机名称','alias':'my-phone'},session,method='PATCH')
alias=must('/api/v1/push',{'body':'别名目标','device_ids':['my-phone']},keys['native']['token'])
assert must('/api/v1/messages/'+alias['message_id'],token=keys['native']['token'])['deliveries'][0]['device_name']=='友好的手机名称'
# Public compatibility management preserves credential isolation and real state.
secret_key=os.urandom(24).hex()
must('/api/v1/manage/keys/access',{'id':keys['pushplus']['id'],'secret':secret_key,'enabled':True},session)
access=must('/api/common/openApi/getAccessKey',{'token':keys['pushplus']['token'],'secretKey':secret_key})['data']['accessKey']
headers={'access-key':access}
listed=must('/api/open/message/list',{},headers=headers)['data']
assert listed['total']==1
short=listed['list'][0]['shortCode']
state=must('/api/open/message/sendMessageResult?shortCode='+short,headers=headers)
assert state['data']['status']==2 and state['extension']['received_devices']==0
must('/api/open/message/deleteMessage?shortCode='+short,method='DELETE',headers=headers)
assert request('/shortMessage/'+short)[0]==410
assert must('/api/open/message/list',{},headers=headers)['data']['total']==0
wx=next(m for m in messages if m['protocol']=='wxpusher')
wx_content=must('/api/v1/manage/messages/'+wx['id'],token=session)['message']['message_content_id']
must('/api/send/message?appToken='+keys['wxpusher']['token']+'&messageContentId='+str(wx_content),method='DELETE')
assert request('/wxpusher/message/'+wx['id'])[0]==410
retired=must('/api/send/query/status?sendRecordId=undefined')
assert retired['code']==1001 and not retired['success']
qr=must('/api/fun/create/qrcode',{'appToken':keys['wxpusher']['token'],'extra':'integration-extra','validTime':1800})['data']
claim={'code':qr['code'],'device_id':paired[1]['device_id'],'topic_ids':[topic['id']]}
assert request('/api/v1/manage/subscriptions/claim',claim)[0]==401
must('/api/v1/manage/subscriptions/claim',claim,session)
assert must('/api/fun/scan-qrcode-uid?code='+qr['code'])['data']==paired[1]['device_id']
# Revocation applies in ntfy, not only in the gateway's database.
must('/api/v1/manage/keys',{'id':native['id'],'disabled':True},session,method='PATCH')
assert request('/'+group,'revoked',native['token'],headers={'Host':'ntfy.localhost'})[0]==401
must('/api/v1/manage/devices',{'id':paired[0]['device_id'],'disabled':True},session,method='PATCH')
assert request('/'+paired[0]['topic']+'/json?poll=1',token=paired[0]['ntfy_token'],headers={'Host':'ntfy.localhost'},raw=True)[0]==401
result={'result':'passed','protocols':7,'devices':2,'checks':['one-time pairing','all adapters publish via real ntfy','private cache authentication','cross-device isolation','native group fanout','native actions preserved','native attachment accessible','long Chinese body preserved','business metadata','authenticated phone receipts','persistent delivery events','friendly device alias','sender status scope','PushPlus AccessKey and scoped lifecycle','WxPusher landing deletion','deprecated query response','authenticated QR subscription','upstream key and device revocation'],'firebase':'not configured; no cloud delivery claimed','test_user':name}
print(json.dumps(result,ensure_ascii=False,indent=2))
