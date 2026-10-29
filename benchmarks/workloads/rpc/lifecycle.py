"""Define process-session cleanup helpers for use by the benchmark controllers."""
import os,signal,subprocess,time
from pathlib import Path

def need(x,m):
 if not x:raise ValueError(m)

def members(sid):
 out=[]
 for p in Path('/proc').glob('[0-9]*/stat'):
  try:
   a=p.read_text().rsplit(')',1)[1].split()
   if a[0]!='Z' and int(a[3])==sid:out.append({'pid':int(p.parent.name),'pgid':int(a[2]),'starttime':a[19]})
  except FileNotFoundError:pass
 return out

def retire(p):
 if p.birth is None:
  need(p.poll() is not None and not members(p.pid),'Unconfirmed live birth; no signal issued')
 else:
  for sig,seconds in ((signal.SIGTERM,3),(signal.SIGKILL,5)):
   live=members(p.pid)
   if p.poll() is not None and not live:break
   leader=[x for x in live if x['pid']==p.pid]
   need(not leader or leader[0]['starttime']==p.birth,'Recycled session leader')
   groups={x['pgid'] for x in live}
   if p.poll() is None:groups.add(p.pid)
   for pgid in groups:
    try:os.killpg(pgid,sig)
    except ProcessLookupError:pass
   end=time.monotonic()+seconds
   while time.monotonic()<end:
    if p.poll() is not None and not members(p.pid):break
    time.sleep(.1)
 p.wait(timeout=1)
 return {'pid':p.pid,'birth':p.birth,'returncode':p.poll(),'remaining':members(p.pid)}

