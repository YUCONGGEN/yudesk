// Test-only fake Go core. No AppKit, network, display, or user data access.
#include <sys/file.h>
#include <sys/stat.h>
#include <unistd.h>
#include <signal.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <limits.h>
static volatile sig_atomic_t done;
static void stop(int value) { (void)value; done = 1; }
int main(void) {
    const char *root = getenv("YUDESK_LAUNCHER_FIXTURE_STATE");
    if (!root || root[0] != '/') return 2;
    char path[PATH_MAX];
    if (snprintf(path,sizeof(path),"%s/core.lock",root) >= (int)sizeof(path)) return 2;
    int lock = open(path,O_CREAT|O_RDWR,0600); if (lock < 0) return 2;
    int secondary = flock(lock, LOCK_EX|LOCK_NB) != 0;
    if (snprintf(path,sizeof(path),"%s/events",root) >= (int)sizeof(path)) return 2;
    FILE *events = fopen(path,"a"); if (!events) return 2;
    fprintf(events,"%s %d %d\n",secondary ? "secondary" : "primary",getpid(),getppid()); fclose(events);
    if (secondary) { close(lock); return 0; }
    signal(SIGTERM,stop); signal(SIGINT,stop);
    for (int n=0; !done && n<600; ++n) usleep(100000);
    close(lock); return 0;
}
