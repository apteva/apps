/* Test-only adapter. The BSD-licensed upstream source is unchanged. */
#include <stdio.h>
#include <stdint.h>
#include <stdlib.h>
#include "speex/speex_jitter.h"

typedef struct { long long at; uint32_t seq,ts; } Input;
int main(void) {
 int count=0;
 if(scanf("%d",&count)!=1 || count<1 || count>1000000) return 2;
 Input *p=calloc((size_t)count,sizeof(*p));
 if(!p) return 3;
 for(int i=0;i<count;i++) if(scanf("%lld %u %u",&p[i].at,&p[i].seq,&p[i].ts)!=3) return 4;
 JitterBuffer *j=jitter_buffer_init(160);
 int margin=480; jitter_buffer_ctl(j,JITTER_BUFFER_SET_MARGIN,&margin);
 int next=0;
 for(long long tick=0;tick<=p[count-1].at+500000000LL;tick+=20000000LL) {
  while(next<count && p[next].at<=tick) {
   JitterBufferPacket packet={0}; packet.data=(char*)&p[next].seq;
   packet.len=sizeof(p[next].seq); packet.timestamp=p[next].ts;
   packet.span=160; packet.sequence=p[next].seq;
   jitter_buffer_put(j,&packet); next++;
  }
  uint32_t seq=0; JitterBufferPacket out={0}; out.data=(char*)&seq; out.len=sizeof(seq);
  spx_int32_t offset=0;
  int result=jitter_buffer_get(j,&out,160,&offset);
  printf("%lld %d %u %u %d\n",tick,result,seq,out.timestamp,offset);
  jitter_buffer_tick(j);
 }
 jitter_buffer_destroy(j); free(p); return 0;
}
