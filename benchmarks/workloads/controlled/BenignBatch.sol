pragma solidity =0.5.16;

interface IBenignPermit {
    function permit(address owner, address spender, uint value, uint deadline, uint8 v, bytes32 r, bytes32 s) external;
}
interface IBenignRun {
    function run(uint n, uint8 kind, uint deadline, uint8 v, bytes32 r, bytes32 s) external returns (bytes32);
}

contract BenignBatch {
    IBenignPermit public token;
    address public owner;
    uint[64] public entries;
    event BatchResult(uint indexed length, bytes32 checksum);

    constructor(address tokenAddress, address permitOwner) public {
        token = IBenignPermit(tokenAddress);
        owner = permitOwner;
        for (uint i = 0; i < 64; i++) entries[i] = 1;
    }
    function run(uint n, uint8 kind, uint deadline, uint8 v, bytes32 r, bytes32 s) external returns (bytes32) {
        require(n == 1 || n == 8 || n == 64, "length");
        require(kind <= 2, "kind");
        if (kind == 1) token.permit(owner, address(this), 1, deadline, v, r, s);
        bytes32 checksum;
        for (uint i = 0; i < n; i++) {
            uint updated = entries[i] + 1;
            entries[i] = updated;
            checksum = keccak256(abi.encodePacked(checksum, i, updated));
        }
        emit BatchResult(n, checksum);
        if (kind == 2) token.permit(owner, address(this), 1, deadline, v, r, s);
        return checksum;
    }
}

contract BenignForwarder {
    IBenignRun public next;
    constructor(address destination) public { next = IBenignRun(destination); }
    function run(uint n, uint8 kind, uint deadline, uint8 v, bytes32 r, bytes32 s) external returns (bytes32) {
        return next.run(n, kind, deadline, v, r, s);
    }
}
