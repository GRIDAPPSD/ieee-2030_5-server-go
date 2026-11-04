import { join } from 'path';

// The one definition of where the sep2server binary lives: globalSetup builds
// it here, setup.ts runs it, and specs that mint certificates exec it.
export const SERVER_BINARY = join(__dirname, '..', process.platform === 'win32' ? 'sep2server.exe' : 'sep2server');
