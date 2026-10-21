#ifndef MYMAC_MENUBAR_DARWIN_H
#define MYMAC_MENUBAR_DARWIN_H

// mymac_menubar_create builds the status item and its menu. It returns 1 on
// success and 0 when the status bar is not available.
int mymac_menubar_create(const char *title, const char *address);

// mymac_menubar_run enters the Cocoa event loop. It returns once the loop
// stops.
void mymac_menubar_run(void);

#endif
