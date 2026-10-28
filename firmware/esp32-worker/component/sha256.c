#include "job_core.h"
#include "sha256_internal.h"

#include <stdint.h>
#include <string.h>

static const uint32_t k[64] = {
    0x428a2f98u,0x71374491u,0xb5c0fbcfu,0xe9b5dba5u,0x3956c25bu,0x59f111f1u,0x923f82a4u,0xab1c5ed5u,
    0xd807aa98u,0x12835b01u,0x243185beu,0x550c7dc3u,0x72be5d74u,0x80deb1feu,0x9bdc06a7u,0xc19bf174u,
    0xe49b69c1u,0xefbe4786u,0x0fc19dc6u,0x240ca1ccu,0x2de92c6fu,0x4a7484aau,0x5cb0a9dcu,0x76f988dau,
    0x983e5152u,0xa831c66du,0xb00327c8u,0xbf597fc7u,0xc6e00bf3u,0xd5a79147u,0x06ca6351u,0x14292967u,
    0x27b70a85u,0x2e1b2138u,0x4d2c6dfcu,0x53380d13u,0x650a7354u,0x766a0abbu,0x81c2c92eu,0x92722c85u,
    0xa2bfe8a1u,0xa81a664bu,0xc24b8b70u,0xc76c51a3u,0xd192e819u,0xd6990624u,0xf40e3585u,0x106aa070u,
    0x19a4c116u,0x1e376c08u,0x2748774cu,0x34b0bcb5u,0x391c0cb3u,0x4ed8aa4au,0x5b9cca4fu,0x682e6ff3u,
    0x748f82eeu,0x78a5636fu,0x84c87814u,0x8cc70208u,0x90befffau,0xa4506cebu,0xbef9a3f7u,0xc67178f2u
};

static uint32_t rotr(uint32_t x, unsigned n) { return (x >> n) | (x << (32u - n)); }

static void transform(ed_sha256_context *c, const unsigned char *p) {
    uint32_t w[64], a,b,d,e,f,g,h,t1,t2,cc;
    size_t i;
    for (i=0; i<16; i++) w[i] = ((uint32_t)p[4*i]<<24)|((uint32_t)p[4*i+1]<<16)|((uint32_t)p[4*i+2]<<8)|p[4*i+3];
    for (; i<64; i++) {
        uint32_t s0=rotr(w[i-15],7)^rotr(w[i-15],18)^(w[i-15]>>3);
        uint32_t s1=rotr(w[i-2],17)^rotr(w[i-2],19)^(w[i-2]>>10);
        w[i]=w[i-16]+s0+w[i-7]+s1;
    }
    a=c->h[0]; b=c->h[1]; cc=c->h[2]; d=c->h[3]; e=c->h[4]; f=c->h[5]; g=c->h[6]; h=c->h[7];
    for (i=0; i<64; i++) {
        uint32_t s1=rotr(e,6)^rotr(e,11)^rotr(e,25), ch=(e&f)^(~e&g);
        uint32_t s0=rotr(a,2)^rotr(a,13)^rotr(a,22), maj=(a&b)^(a&cc)^(b&cc);
        t1=h+s1+ch+k[i]+w[i]; t2=s0+maj;
        h=g; g=f; f=e; e=d+t1; d=cc; cc=b; b=a; a=t1+t2;
    }
    c->h[0]+=a;c->h[1]+=b;c->h[2]+=cc;c->h[3]+=d;c->h[4]+=e;c->h[5]+=f;c->h[6]+=g;c->h[7]+=h;
}

static void init(ed_sha256_context *c) {
    static const uint32_t initial[8]={0x6a09e667u,0xbb67ae85u,0x3c6ef372u,0xa54ff53au,0x510e527fu,0x9b05688cu,0x1f83d9abu,0x5be0cd19u};
    memcpy(c->h,initial,sizeof(initial)); c->bits=0; c->used=0;
}

static void update(ed_sha256_context *c, const unsigned char *data, size_t length) {
    size_t n;
    while (length) {
        n=64-c->used; if (n>length) n=length;
        memcpy(c->block+c->used,data,n); c->used+=n; data+=n; length-=n; c->bits+=(uint64_t)n*8u;
        if (c->used==64) { transform(c,c->block); c->used=0; }
    }
}

static void finish(ed_sha256_context *c, unsigned char out[32]) {
    uint64_t bits=c->bits; size_t i;
    c->block[c->used++]=0x80;
    if (c->used>56) { memset(c->block+c->used,0,64-c->used); transform(c,c->block); c->used=0; }
    memset(c->block+c->used,0,56-c->used);
    for (i=0;i<8;i++) c->block[63-i]=(unsigned char)(bits>>(8*i));
    transform(c,c->block);
    for (i=0;i<8;i++) { out[4*i]=(unsigned char)(c->h[i]>>24);out[4*i+1]=(unsigned char)(c->h[i]>>16);out[4*i+2]=(unsigned char)(c->h[i]>>8);out[4*i+3]=(unsigned char)c->h[i]; }
}

void ed_sha256(const unsigned char *bytes, size_t length, unsigned char digest_out[32]) {
    ed_sha256_context c; init(&c); if (length) update(&c,bytes,length); finish(&c,digest_out);
}

/* Internal incremental interface used by job_core.c. */
void ed_sha256_init_internal(ed_sha256_context *context) { init(context); }
void ed_sha256_update_internal(ed_sha256_context *context,const unsigned char *bytes,size_t length) { update(context,bytes,length); }
void ed_sha256_finish_internal(ed_sha256_context *context,unsigned char digest[32]) { finish(context,digest); }
